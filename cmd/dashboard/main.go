package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

//go:embed web/*
var webFS embed.FS

// defaultPasswordHash is the SHA-256 fallback (used only when KOPTIMIZER_PASSWORD_HASH is not set).
const defaultPasswordHash = "5bfda4d20d741f883f24f2286870f9bebf6e2f755e518f6d7c06b2533bee0b0b"

// passwordMode indicates which hashing scheme is in use.
type passwordMode int

const (
	modeSHA256 passwordMode = iota
	modeBcrypt
)

var (
	activePasswordMode passwordMode
	sha256Hash         string
	bcryptHash         []byte
)

func initPassword() {
	envHash := os.Getenv("KOPTIMIZER_PASSWORD_HASH")
	if envHash != "" {
		// If it starts with "$2" it's a bcrypt hash
		if strings.HasPrefix(envHash, "$2") {
			activePasswordMode = modeBcrypt
			bcryptHash = []byte(envHash)
			log.Println("Password loaded from KOPTIMIZER_PASSWORD_HASH (bcrypt)")
			return
		}
		// Otherwise treat as SHA-256 hex hash
		activePasswordMode = modeSHA256
		sha256Hash = envHash
		log.Println("Password loaded from KOPTIMIZER_PASSWORD_HASH (sha256)")
		return
	}
	activePasswordMode = modeSHA256
	sha256Hash = defaultPasswordHash
	log.Println("Using default password hash (set KOPTIMIZER_PASSWORD_HASH env var for per-cluster passwords)")
}

// session holds a token with its creation time.
type session struct {
	createdAt time.Time
}

const (
	maxSessions    = 1000
	sessionTTL     = 8 * time.Hour
	cleanupInterval = 5 * time.Minute
)

// sessionStore holds valid session tokens with expiry.
var sessionStore = struct {
	sync.RWMutex
	tokens map[string]session
}{tokens: make(map[string]session)}

// loginLimiter provides per-IP rate limiting for login attempts.
var loginLimiter = struct {
	sync.Mutex
	attempts map[string][]time.Time
}{attempts: make(map[string][]time.Time)}

const (
	maxLoginAttempts = 5
	loginWindow      = 1 * time.Minute
)

func main() {
	apiURL := flag.String("api-url", "http://localhost:8080", "KOptimizer API URL")
	port := flag.Int("port", 3000, "Dashboard port")
	flag.Parse()

	initPassword()

	// Start session cleanup goroutine
	go sessionCleanup()

	target, err := url.Parse(*apiURL)
	if err != nil {
		log.Fatalf("invalid api-url: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	// Inject API bearer token into proxied requests for backend authentication.
	apiToken := os.Getenv("KOPTIMIZER_API_TOKEN")
	if apiToken != "" {
		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			req.Header.Set("Authorization", "Bearer "+apiToken)
		}
		log.Println("API proxy: bearer-token injection enabled")
	}

	webContent, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("failed to create sub filesystem: %v", err)
	}
	fileServer := http.FileServer(http.FS(webContent))

	indexHTML, err := webFS.ReadFile("web/index.html")
	if err != nil {
		log.Fatalf("failed to read index.html: %v", err)
	}

	mux := http.NewServeMux()

	// Auth endpoints (no auth required)
	mux.HandleFunc("/auth/login", handleLogin)
	mux.HandleFunc("/auth/check", handleAuthCheck)
	mux.HandleFunc("/auth/logout", handleLogout)

	// Proxy API requests to the KOptimizer API
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	})

	// Serve static files, with SPA fallback to index.html
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(indexHTML)
			return
		}

		// Try to serve the static file
		if _, err := fs.Stat(webContent, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}

		// SPA fallback: return index.html for unmatched paths
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})

	// Apply middleware: security headers then auth
	handler := securityHeaders(authMiddleware(mux))

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("KOptimizer Dashboard listening on %s (API proxy -> %s)", addr, *apiURL)

	server := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func checkPassword(password string) bool {
	switch activePasswordMode {
	case modeBcrypt:
		return bcrypt.CompareHashAndPassword(bcryptHash, []byte(password)) == nil
	default:
		h := sha256.Sum256([]byte(password))
		got := []byte(hex.EncodeToString(h[:]))
		want := []byte(sha256Hash)
		return subtle.ConstantTimeCompare(got, want) == 1
	}
}

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func isAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie("katalyst_session")
	if err != nil {
		return false
	}
	sessionStore.RLock()
	defer sessionStore.RUnlock()
	sess, ok := sessionStore.tokens[cookie.Value]
	if !ok {
		return false
	}
	// Check TTL
	if time.Since(sess.createdAt) > sessionTTL {
		return false
	}
	return true
}

// isTLS returns true if the request appears to be over TLS.
func isTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	// Check common headers set by reverse proxies / load balancers
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}

// clientIP extracts the client IP from the request.
func clientIP(r *http.Request) string {
	// Check X-Forwarded-For first (trust in-cluster proxy)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isRateLimited returns true if the IP has exceeded login attempt limits.
func isRateLimited(ip string) bool {
	loginLimiter.Lock()
	defer loginLimiter.Unlock()

	now := time.Now()
	cutoff := now.Add(-loginWindow)

	// Clean old attempts
	attempts := loginLimiter.attempts[ip]
	valid := attempts[:0]
	for _, t := range attempts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	loginLimiter.attempts[ip] = valid

	return len(valid) >= maxLoginAttempts
}

// recordLoginAttempt records a failed login attempt for rate limiting.
func recordLoginAttempt(ip string) {
	loginLimiter.Lock()
	defer loginLimiter.Unlock()
	loginLimiter.attempts[ip] = append(loginLimiter.attempts[ip], time.Now())
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ip := clientIP(r)
	if isRateLimited(ip) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]string{"error": "too many login attempts, try again later"})
		return
	}

	// Limit request body size
	r.Body = http.MaxBytesReader(w, r.Body, 4096)

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !checkPassword(req.Password) {
		recordLoginAttempt(ip)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid password"})
		return
	}

	token := generateToken()
	sessionStore.Lock()
	// Enforce max sessions: if at limit, evict oldest
	if len(sessionStore.tokens) >= maxSessions {
		evictOldestSession()
	}
	sessionStore.tokens[token] = session{createdAt: time.Now()}
	sessionStore.Unlock()

	cookie := &http.Cookie{
		Name:     "katalyst_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	}
	if isTLS(r) {
		cookie.Secure = true
	}
	http.SetCookie(w, cookie)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func handleAuthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if isAuthenticated(r) {
		json.NewEncoder(w).Encode(map[string]bool{"authenticated": true})
	} else {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]bool{"authenticated": false})
	}
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if cookie, err := r.Cookie("katalyst_session"); err == nil {
		sessionStore.Lock()
		delete(sessionStore.tokens, cookie.Value)
		sessionStore.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "katalyst_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// authMiddleware protects all routes except /auth/* and static assets needed for the login page.
func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Allow auth endpoints, CSS, JS, and fonts (needed to render login page)
		if strings.HasPrefix(path, "/auth/") ||
			strings.HasPrefix(path, "/css/") ||
			strings.HasPrefix(path, "/js/") ||
			strings.HasSuffix(path, ".woff2") ||
			strings.HasSuffix(path, ".woff") {
			next.ServeHTTP(w, r)
			return
		}

		// For API calls, return 401 JSON
		if strings.HasPrefix(path, "/api/") {
			if !isAuthenticated(r) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// For page requests, always serve index.html (the JS app handles login UI)
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' https://cdn.jsdelivr.net; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' https://fonts.gstatic.com; "+
				"img-src 'self' data:; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		// HSTS: enforce HTTPS for 1 year, include subdomains
		if isTLS(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// sessionCleanup periodically removes expired sessions.
func sessionCleanup() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		sessionStore.Lock()
		for token, sess := range sessionStore.tokens {
			if now.Sub(sess.createdAt) > sessionTTL {
				delete(sessionStore.tokens, token)
			}
		}
		sessionStore.Unlock()

		// Also clean up old rate limit entries
		loginLimiter.Lock()
		cutoff := now.Add(-loginWindow)
		for ip, attempts := range loginLimiter.attempts {
			valid := attempts[:0]
			for _, t := range attempts {
				if t.After(cutoff) {
					valid = append(valid, t)
				}
			}
			if len(valid) == 0 {
				delete(loginLimiter.attempts, ip)
			} else {
				loginLimiter.attempts[ip] = valid
			}
		}
		loginLimiter.Unlock()
	}
}

// evictOldestSession removes the oldest session. Caller must hold sessionStore.Lock().
func evictOldestSession() {
	var oldestToken string
	var oldestTime time.Time
	for token, sess := range sessionStore.tokens {
		if oldestToken == "" || sess.createdAt.Before(oldestTime) {
			oldestToken = token
			oldestTime = sess.createdAt
		}
	}
	if oldestToken != "" {
		delete(sessionStore.tokens, oldestToken)
	}
}
