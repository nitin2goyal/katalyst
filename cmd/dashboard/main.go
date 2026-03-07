package main

import (
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
)

//go:embed web/*
var webFS embed.FS

// SHA-256 hash of the dashboard password.
const passwordHash = "5bfda4d20d741f883f24f2286870f9bebf6e2f755e518f6d7c06b2533bee0b0b"

// sessionStore holds valid session tokens.
var sessionStore = struct {
	sync.RWMutex
	tokens map[string]bool
}{tokens: make(map[string]bool)}

func main() {
	apiURL := flag.String("api-url", "http://localhost:8080", "KOptimizer API URL")
	port := flag.Int("port", 3000, "Dashboard port")
	flag.Parse()

	target, err := url.Parse(*apiURL)
	if err != nil {
		log.Fatalf("invalid api-url: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

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
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func checkPassword(password string) bool {
	h := sha256.Sum256([]byte(password))
	return hex.EncodeToString(h[:]) == passwordHash
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
	return sessionStore.tokens[cookie.Value]
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !checkPassword(req.Password) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid password"})
		return
	}
	token := generateToken()
	sessionStore.Lock()
	sessionStore.tokens[token] = true
	sessionStore.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     "katalyst_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
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
	if cookie, err := r.Cookie("katalyst_session"); err == nil {
		sessionStore.Lock()
		delete(sessionStore.tokens, cookie.Value)
		sessionStore.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:   "katalyst_session",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
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
				"script-src 'self' https://cdn.jsdelivr.net 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"connect-src 'self'; "+
				"font-src 'self'")
		next.ServeHTTP(w, r)
	})
}
