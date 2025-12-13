package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yaffw/yaffw/src/internal/adapters/http_queue"
	"github.com/yaffw/yaffw/src/internal/adapters/memory"
	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
	"github.com/yaffw/yaffw/src/internal/services"
)

var playbackSvc *services.PlaybackService

func main() {
	log.Println("Starting yaffw Control Plane...")

	// 1. Initialize Adapters
	var mediaRepo ports.MediaRepository
	var jobQueue ports.JobQueue

	runMode := os.Getenv("RUN_MODE")
	cwd, _ := os.Getwd()

	if runMode == "test" || runMode == "memory" {
		log.Println("Running in MEMORY/TEST mode")
		repo := memory.NewMediaRepo()
		mediaRepo = repo

		// Initialize In-Memory Job Queue (Master Source of Truth)
		queue := memory.NewJobQueue()
		jobQueue = queue

		// Seed data from 'data' directory
		scanner := services.NewLibraryScanner(repo)
		dataDir := os.Getenv("DATA_DIR")
		if dataDir == "" {
			dataDir = "data"
		}

		log.Printf("Scanning media from: %s", dataDir)
		if err := scanner.ScanDirectory(context.Background(), dataDir); err != nil {
			log.Printf("Warning: Failed to scan directory: %v", err)
		}
	} else {
		log.Println("Running in PRODUCTION mode (Postgres pending implementation)")
		mediaRepo = memory.NewMediaRepo()
		jobQueue = memory.NewJobQueue()
	}

	// Initialize Playback Service
	ffmpegPath := "ffmpeg"
	playbackSvc = services.NewPlaybackService(mediaRepo, ffmpegPath)

	// Start Job Reaper
	reaper := services.NewJobReaper(jobQueue)
	go reaper.StartMonitoring(context.Background())

	// 2. Expose Queue via HTTP (Mock Redis)
	mux := http.NewServeMux()
	if runMode == "test" || runMode == "memory" {
		queueServer := http_queue.NewHTTPServerQueue(jobQueue)
		queueServer.RegisterHandlers(mux)
		log.Println("Exposing internal queue at /internal/queue/...")
	}

	// 3. Setup UI & API Handlers
	tmplPath := filepath.Join(cwd, "src", "ControlPlane", "templates", "index.html")
	if _, err := os.Stat(tmplPath); os.IsNotExist(err) {
		tmplPath = filepath.Join(cwd, "templates", "index.html")
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		tmpl, err := template.ParseFiles(tmplPath)
		if err != nil {
			http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		items, err := mediaRepo.ListAll(r.Context())
		if err != nil {
			http.Error(w, "DB Error: "+err.Error(), http.StatusInternalServerError)
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

	// HLS Segment/Manifest Server (Reverse Proxy to Worker)
	mux.Handle("/hls/", http.StripPrefix("/hls/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path is now "job-ID/stream.m3u8" or "job-ID/segment_001.ts"
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 1 {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}
		jobID := parts[0]

		// 1. Get Job to find Worker Address
		job, err := jobQueue.GetJob(r.Context(), jobID)
		if err != nil {
			http.Error(w, "Job not found", http.StatusNotFound)
			return
		}

		if job.WorkerAddress == "" {
			http.Error(w, "Worker address not found for job", http.StatusBadGateway)
			return
		}

		// 2. Proxy Request
		targetURL, err := url.Parse(job.WorkerAddress)
		if err != nil {
			http.Error(w, "Invalid worker address", http.StatusInternalServerError)
			return
		}

		proxy := httputil.NewSingleHostReverseProxy(targetURL)
		proxy.ServeHTTP(w, r)
	})))

	// Smart Playback API
	mux.HandleFunc("/api/playback/", func(w http.ResponseWriter, r *http.Request) {
		handlePlaybackRequest(w, r, mediaRepo, jobQueue)
	})

	// Stream Handlers (Direct Play + HLS)
	mux.HandleFunc("/stream/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			handleHLS(w, r, mediaRepo, jobQueue)
			return
		}

		// Direct Stream
		id := r.URL.Path[len("/stream/"):]
		item, err := mediaRepo.GetByID(r.Context(), id)
		if err != nil {
			http.Error(w, "Media not found", http.StatusNotFound)
			return
		}
		log.Printf("Direct Streaming %s", item.Title)
		http.ServeFile(w, r, item.Path)
	})

	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Pong"))
	})

	// Client Logging Endpoint
	mux.HandleFunc("/client-log", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			return
		}
		var msg map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&msg); err == nil {
			log.Printf("[CLIENT] %s: %v", msg["level"], msg["message"])
		}
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8096"
	}

	log.Printf("Control Plane listening on http://0.0.0.0:%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}

// PlaybackRequest from client
type PlaybackRequest struct {
	MediaID      string                    `json:"mediaId"`
	Capabilities domain.ClientCapabilities `json:"capabilities"`
}

// PlaybackResponse to client
type PlaybackResponse struct {
	Type      string `json:"type"`      // "direct" or "transcode"
	URL       string `json:"url"`       // Stream URL
	MediaType string `json:"mediaType"` // MIME type hint
}

func handlePlaybackRequest(w http.ResponseWriter, r *http.Request, repo ports.MediaRepository, queue ports.JobQueue) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	var req PlaybackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Get media item
	item, err := repo.GetByID(r.Context(), req.MediaID)
	if err != nil {
		http.Error(w, "Media not found", http.StatusNotFound)
		return
	}

	// Probe the media file
	probe, err := playbackSvc.ProbeMedia(r.Context(), item.Path)
	if err != nil {
		log.Printf("Failed to probe %s: %v", item.Path, err)
		// Fall back to transcoding if probe fails
		resp := PlaybackResponse{
			Type:      "transcode",
			URL:       "/stream/" + req.MediaID + "/master.m3u8",
			MediaType: "application/vnd.apple.mpegurl",
		}
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Determine playback strategy
	strategy := playbackSvc.DeterminePlaybackStrategy(probe, &req.Capabilities)

	if !strategy.NeedsTranscode {
		// Direct Play!
		log.Printf("Direct Play for %s (video=%s, audio=%s)", item.Title, probe.VideoCodec, probe.AudioCodec)
		resp := PlaybackResponse{
			Type:      "direct",
			URL:       "/stream/" + req.MediaID,
			MediaType: "video/" + probe.Container,
		}
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Needs Transcode - one job per media item
	jobID := "job-" + req.MediaID

	// Check if job already exists
	existingJob, err := queue.GetJob(r.Context(), jobID)
	if err != nil {
		// Create new job with strategy settings
		log.Printf("Queuing Transcode Job for %s (video: %s->%s, audio: %s->%s)",
			item.Title, probe.VideoCodec, strategy.TargetVideoCodec,
			probe.AudioCodec, strategy.TargetAudioCodec)

		newJob := &domain.TranscodeJob{
			ID:               jobID,
			MediaID:          item.ID,
			FilePath:         item.Path,
			Status:           "Pending",
			TargetVideoCodec: strategy.TargetVideoCodec,
			TargetAudioCodec: strategy.TargetAudioCodec,
			TargetContainer:  strategy.TargetContainer,
			NeedsTranscode:   true,
		}
		queue.Enqueue(r.Context(), newJob)
		existingJob = newJob
	}

	// Return transcode URL
	resp := PlaybackResponse{
		Type:      "transcode",
		URL:       fmt.Sprintf("/hls/%s/stream.m3u8", jobID),
		MediaType: "application/vnd.apple.mpegurl",
	}

	// If job is already ready, use its stream URL
	if existingJob.Status == "Ready" && existingJob.StreamURL != "" {
		resp.URL = existingJob.StreamURL
	}

	json.NewEncoder(w).Encode(resp)
}

func handleHLS(w http.ResponseWriter, r *http.Request, repo ports.MediaRepository, queue ports.JobQueue) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 3 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	id := parts[2]
	jobID := "job-" + id

	// Check/Create Job
	job, err := queue.GetJob(r.Context(), jobID)
	if err != nil {
		item, err := repo.GetByID(r.Context(), id)
		if err != nil {
			http.Error(w, "Media not found", http.StatusNotFound)
			return
		}

		log.Printf("Queuing Transcode Job for %s (fallback, no capabilities provided)", item.Title)
		newJob := &domain.TranscodeJob{
			ID:               jobID,
			MediaID:          item.ID,
			FilePath:         item.Path,
			Status:           "Pending",
			TargetVideoCodec: "libx264",
			TargetAudioCodec: "aac",
			TargetContainer:  "hls",
			NeedsTranscode:   true,
		}
		queue.Enqueue(r.Context(), newJob)
		job = newJob
	}

	// Poll Status
	timeout := time.After(30 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for job.Status != "Ready" {
		select {
		case <-timeout:
			http.Error(w, "Transcode timeout - Is Compute Plane running?", http.StatusGatewayTimeout)
			return
		case <-ticker.C:
			j, err := queue.GetJob(r.Context(), jobID)
			if err == nil {
				job = j
			}
		}
	}

	log.Printf("Job Ready! Redirecting to %s", job.StreamURL)
	http.Redirect(w, r, job.StreamURL, http.StatusFound)
}
