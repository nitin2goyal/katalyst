package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

//go:embed web/*
var webFS embed.FS

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

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("KOptimizer Dashboard listening on %s (API proxy -> %s)", addr, *apiURL)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
