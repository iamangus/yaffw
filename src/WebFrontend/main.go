package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type MediaItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Path      string `json:"path"`
	PosterURL string `json:"posterUrl,omitempty"`
}

func main() {
	log.Println("Starting yaffw Web Frontend...")

	cpURL := os.Getenv("CONTROL_PLANE_URL")
	if cpURL == "" {
		cpURL = "http://localhost:8096"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	cwd, _ := os.Getwd()
	// Try to find the template in likely locations
	tmplPath := filepath.Join(cwd, "src", "WebFrontend", "templates", "index.html")
	if _, err := os.Stat(tmplPath); os.IsNotExist(err) {
		// Fallback if running from src/WebFrontend or similar
		tmplPath = "templates/index.html"
	}

	mux := http.NewServeMux()

	// 1. Home Page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		// Fetch media from Control Plane
		// Note: We are using the new API endpoint defined in the separation plan
		resp, err := http.Get(cpURL + "/api/v1/media")
		if err != nil {
			http.Error(w, "Failed to fetch media from Control Plane: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			http.Error(w, fmt.Sprintf("Control Plane returned %d", resp.StatusCode), http.StatusBadGateway)
			return
		}

		var items []MediaItem
		if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
			http.Error(w, "Failed to decode media list: "+err.Error(), http.StatusInternalServerError)
			return
		}

		tmpl, err := template.ParseFiles(tmplPath)
		if err != nil {
			http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		data := map[string]interface{}{
			"Time":             time.Now().Format(time.RFC3339),
			"ContinueWatching": items,
		}

		if err := tmpl.Execute(w, data); err != nil {
			log.Printf("Error executing template: %v", err)
		}
	})

	// 2. Proxy API requests to Control Plane
	target, err := url.Parse(cpURL)
	if err != nil {
		log.Fatal("Invalid CONTROL_PLANE_URL: ", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
	}

	// Proxy API, Stream, and HLS endpoints
	mux.Handle("/api/", proxy)
	mux.Handle("/stream/", proxy)
	mux.Handle("/hls/", proxy)

	// 3. Client Logging
	mux.HandleFunc("/client-log", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			return
		}
		var msg map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&msg); err == nil {
			log.Printf("[CLIENT] %s: %v", msg["level"], msg["message"])
		}
	})

	log.Printf("Web Frontend listening on http://0.0.0.0:%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}