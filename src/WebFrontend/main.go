package main

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/yaffw/yaffw/src/internal/config"
)

type MediaItem struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Path         string `json:"path"`
	PosterURL    string `json:"posterUrl,omitempty"`
	ViewProgress *struct {
		Position float64 `json:"position"`
		Finished bool    `json:"finished"`
	} `json:"viewProgress,omitempty"`
}

func main() {
	log.Println("Starting yaffw Web Frontend...")

	// Load Config
	configFile := "config.yaml"
	if len(os.Args) > 1 {
		configFile = os.Args[1]
	}

	var cfg config.WebFrontendConfig
	if err := config.Load(configFile, &cfg); err != nil {
		log.Printf("Warning: Failed to load config '%s': %v. Using defaults/env.", configFile, err)
		cfg.ControlPlaneURL = os.Getenv("CONTROL_PLANE_URL")
		if cfg.ControlPlaneURL == "" {
			cfg.ControlPlaneURL = "http://localhost:8096"
		}
		cfg.Port = os.Getenv("PORT")
		if cfg.Port == "" {
			cfg.Port = "8080"
		}
		cfg.OIDC.ProviderURL = os.Getenv("OIDC_PROVIDER")
		cfg.OIDC.ClientID = os.Getenv("OIDC_CLIENT_ID")
		cfg.OIDC.ClientSecret = os.Getenv("OIDC_CLIENT_SECRET")
		cfg.OIDC.RedirectURL = os.Getenv("OIDC_REDIRECT_URL")
	} else {
		log.Printf("Loaded config from %s", configFile)
	}

	cwd, _ := os.Getwd()
	tmplPath := filepath.Join(cwd, "src", "WebFrontend", "templates", "index.html")
	if _, err := os.Stat(tmplPath); os.IsNotExist(err) {
		tmplPath = "templates/index.html"
	}

	authSvc := NewAuthService(cfg.OIDC)

	mux := http.NewServeMux()

	// Auth Routes
	mux.HandleFunc("/auth/login", authSvc.HandleLogin)
	mux.HandleFunc("/auth/callback", authSvc.HandleCallback)
	mux.HandleFunc("/auth/logout", authSvc.HandleLogout)

	// 1. Home Page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		// Check Auth
		cookie, err := r.Cookie("yaffw_token")
		loggedIn := (err == nil && cookie.Value != "")

		// 1. Fetch Library (All Media)
		// TODO: This should probably be paginated or just a list of IDs for large libraries
		log.Printf("Frontend: Fetching media from %s/api/v1/media", cfg.ControlPlaneURL)
		resp, err := http.Get(cfg.ControlPlaneURL + "/api/v1/media")
		if err != nil {
			log.Printf("Frontend: Failed to fetch media: %v", err)
			http.Error(w, "Failed to fetch media: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		var library []MediaItem
		if err := json.NewDecoder(resp.Body).Decode(&library); err != nil {
			log.Printf("Frontend: Failed to decode library: %v", err)
		} else {
			log.Printf("Frontend: Successfully fetched %d items", len(library))
		}

		// 2. Fetch Continue Watching (Personal) if logged in
		var continueWatching []MediaItem
		if loggedIn {
			req, _ := http.NewRequest("GET", cfg.ControlPlaneURL+"/api/v1/continue-watching", nil)
			req.Header.Set("Authorization", "Bearer "+cookie.Value)

			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Do(req)
			if err == nil && resp.StatusCode == http.StatusOK {
				json.NewDecoder(resp.Body).Decode(&continueWatching)
				resp.Body.Close()
			} else {
				log.Printf("Failed to fetch continue watching: %v", err)
			}
		} else {
			// If not logged in, maybe show random items or nothing?
			// For now, let's just show nothing in Continue Watching to encourage login.
		}

		tmpl, err := template.ParseFiles(tmplPath)
		if err != nil {
			http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		data := map[string]interface{}{
			"Time":             time.Now().Format(time.RFC3339),
			"ContinueWatching": continueWatching,
			"Library":          library,
			"LoggedIn":         loggedIn,
		}

		if err := tmpl.Execute(w, data); err != nil {
			log.Printf("Error executing template: %v", err)
		}
	})

	// 2. Proxy API requests to Control Plane
	target, err := url.Parse(cfg.ControlPlaneURL)
	if err != nil {
		log.Fatal("Invalid CONTROL_PLANE_URL: ", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
	}

	// Proxy with Token Injection
	mux.Handle("/api/", authSvc.TokenMiddleware(proxy))
	mux.Handle("/stream/", authSvc.TokenMiddleware(proxy))
	mux.Handle("/hls/", authSvc.TokenMiddleware(proxy))
	mux.Handle("/artwork/", authSvc.TokenMiddleware(proxy)) // New artwork proxy

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

	log.Printf("Web Frontend listening on http://0.0.0.0:%s", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, mux); err != nil {
		log.Fatal(err)
	}
}
