package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yaffw/yaffw/src/internal/adapters/http_queue"
	"github.com/yaffw/yaffw/src/internal/adapters/postgres"
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

	log.Println("Running in PRODUCTION mode")

	// Connect to Postgres
	connStr := os.Getenv("DATABASE_URL")
	if connStr == "" {
		// Default for dev/docker
		connStr = "postgres://user:password@localhost:5432/yaffw?sslmode=disable"
		log.Printf("No DATABASE_URL set, using default: %s", connStr)
	}

	db, err := postgres.NewConnection(connStr)
	if err != nil {
		log.Fatalf("Failed to connect to Postgres: %v", err)
	}

	// Initialize Postgres Media Repo
	pgMediaRepo := postgres.NewMediaRepo(db)
	if err := pgMediaRepo.InitSchema(); err != nil {
		log.Fatalf("Failed to init media schema: %v", err)
	}
	mediaRepo = pgMediaRepo

	// Initialize Postgres User & History Repos
	pgUserRepo := postgres.NewUserRepo(db)
	if err := pgUserRepo.InitSchema(); err != nil {
		log.Fatalf("Failed to init user schema: %v", err)
	}

	pgHistoryRepo := postgres.NewHistoryRepo(db)
	if err := pgHistoryRepo.InitSchema(); err != nil {
		log.Fatalf("Failed to init history schema: %v", err)
	}

	// Auth Middleware
	authMW := NewAuthMiddleware(pgUserRepo)

	// Initialize Postgres Job Queue
	pgJobQueue := postgres.NewJobQueue(db)
	if err := pgJobQueue.InitSchema(); err != nil {
		log.Fatalf("Failed to init queue schema: %v", err)
	}
	jobQueue = pgJobQueue

	log.Println("Connected to Postgres (MediaRepo + JobQueue)")

	// Seed data from 'data' directory
	scanner := services.NewLibraryScanner(mediaRepo)
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "data"
	}

	log.Printf("Scanning media from: %s", dataDir)
	if err := scanner.ScanDirectory(context.Background(), dataDir); err != nil {
		log.Printf("Warning: Failed to scan directory: %v", err)
	}

	// Initialize Playback Service

	ffmpegPath := "ffmpeg"
	playbackSvc = services.NewPlaybackService(mediaRepo, ffmpegPath)

	// Start Passive Recovery Monitor (JIT-Aware Reaper)
	go startPassiveRecoveryMonitor(context.Background(), jobQueue)

	// Start Session Watchdog (Cleans up idle jobs)
	go startSessionWatchdog(context.Background(), jobQueue)

	// 2. Expose Queue via HTTP
	// We expose this in ALL modes now, so Workers can always connect via HTTP
	// (Even in Prod, avoiding DB creds in workers)
	mux := http.NewServeMux()
	queueServer := http_queue.NewHTTPServerQueue(jobQueue)
	queueServer.RegisterHandlers(mux)
	log.Println("Exposing internal queue at /internal/queue/...")

	// 3. Setup API Handlers

	// List Media API
	mux.HandleFunc("/api/v1/media", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		items, err := mediaRepo.ListAll(r.Context())
		if err != nil {
			http.Error(w, "DB Error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		log.Printf("[API] ListAll returning %d items", len(items))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)
	})

	// Get Media API
	mux.HandleFunc("/api/v1/media/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		id := strings.TrimPrefix(r.URL.Path, "/api/v1/media/")
		if id == "" {
			http.Error(w, "ID required", http.StatusBadRequest)
			return
		}

		item, err := mediaRepo.GetByID(r.Context(), id)
		if err != nil {
			http.Error(w, "Media not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(item)
	})

	// Continue Watching API (Authenticated)
	mux.HandleFunc("/api/v1/continue-watching", authMW.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		userID := GetUserID(r.Context())
		items, err := pgHistoryRepo.ListContinueWatching(r.Context(), userID)
		if err != nil {
			log.Printf("Failed to list continue watching: %v", err)
			http.Error(w, "DB Error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)
	}))

	// Save Progress API (Authenticated)
	mux.HandleFunc("/api/v1/progress", authMW.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}

		var p domain.WatchProgress
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		p.UserID = GetUserID(r.Context())
		p.UpdatedAt = time.Now()

		if err := pgHistoryRepo.SaveProgress(r.Context(), &p); err != nil {
			log.Printf("Failed to save progress: %v", err)
			http.Error(w, "Failed to save", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	// HLS Segment/Manifest Server (Smart Proxy & Playlist Generator)
	mux.Handle("/hls/", http.StripPrefix("/hls/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path is now "job-ID/stream.m3u8" or "job-ID/segment_001.ts"
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 2 {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}
		jobID := parts[0]
		filename := parts[1]

		// 1. Get Job
		job, err := jobQueue.GetJob(r.Context(), jobID)
		if err != nil {
			http.Error(w, "Job not found", http.StatusNotFound)
			return
		}

		// Keep job alive
		jobQueue.TouchJob(r.Context(), jobID)

		// 2. Handle Playlist
		if strings.HasSuffix(filename, ".m3u8") {
			serveDynamicPlaylist(w, job)
			return
		}

		// 3. Handle Segments (Smart Proxy)
		if strings.HasSuffix(filename, ".ts") {
			// Parse segment number: segment_001.ts -> 1
			numStr := strings.TrimSuffix(strings.TrimPrefix(filename, "segment_"), ".ts")
			segNum, err := strconv.Atoi(numStr)
			if err != nil {
				http.Error(w, "Invalid segment name", http.StatusBadRequest)
				return
			}

			// Find segment in job state
			var targetSegment *domain.Segment
			for _, s := range job.Segments {
				if s.SequenceID == segNum {
					targetSegment = &s
					break
				}
			}

			if targetSegment != nil {
				// Segment exists! Proxy to the worker that owns it.
				targetURL, err := url.Parse(targetSegment.WorkerAddr)
				if err != nil {
					http.Error(w, "Invalid worker address", http.StatusInternalServerError)
					return
				}

				log.Printf("[Proxy] Forwarding request for segment %d to worker %s (%s)", segNum, targetSegment.WorkerID, targetURL)

				proxy := httputil.NewSingleHostReverseProxy(targetURL)

				// Handle Proxy Errors (Worker Dead/Unreachable)
				proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
					log.Printf("[Proxy] Failed to proxy segment %d to %s (Worker: %s): %v", segNum, targetURL, targetSegment.WorkerID, err)

					// 1. Mark worker as dead (remove its segments)
					lastKnownSeg, err := jobQueue.MarkWorkerAsDead(r.Context(), jobID, targetSegment.WorkerID)
					if err != nil {
						log.Printf("Failed to mark worker as dead: %v", err)
					}

					// 2. Trigger JIT Recovery (Smart Forward Recovery)
					// If the requested segment is much older than the last known segment,
					// we should resume from the end to keep the stream alive, rather than rewinding.

					recoveryStartSeg := segNum
					if lastKnownSeg > segNum {
						log.Printf("[Recovery] Requested segment %d is older than last known segment %d. Resuming from end to prevent rewind.", segNum, lastKnownSeg)
						recoveryStartSeg = lastKnownSeg + 1
					}

					startTime := float64(recoveryStartSeg) * 4.0
					if err := triggerJITRecovery(r.Context(), jobQueue, job, recoveryStartSeg, startTime); err != nil {
						log.Printf("Failed to trigger recovery: %v", err)
					}

					// 3. Return 503 to client
					w.Header().Set("Retry-After", "2")
					http.Error(w, "Worker unreachable, recovering...", http.StatusServiceUnavailable)
				}

				proxy.ServeHTTP(w, r)
				return
			}

			// Segment MISSING! Trigger JIT Re-transcode.
			log.Printf("[Control] Segment %d missing for job %s. Triggering JIT Re-transcode.", segNum, jobID)

			// Calculate start time for this segment
			startTime := calculateStartTime(job, segNum)

			// Trigger recovery
			if err := triggerJITRecovery(r.Context(), jobQueue, job, segNum, startTime); err != nil {
				log.Printf("Failed to trigger recovery: %v", err)
				http.Error(w, "Recovery failed", http.StatusInternalServerError)
				return
			}

			// Return 503 Service Unavailable with Retry-After header
			// The client should retry, by which time the new worker might be ready.
			w.Header().Set("Retry-After", "2")
			http.Error(w, "Segment regenerating...", http.StatusServiceUnavailable)
			return
		}

		http.Error(w, "Not found", http.StatusNotFound)
	})))

	// Smart Playback API
	mux.HandleFunc("/api/playback/", func(w http.ResponseWriter, r *http.Request) {
		handlePlaybackRequest(w, r, mediaRepo, jobQueue)
	})

	// Session Management API (Stop Transcoding)
	mux.HandleFunc("/api/session/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "DELETE required", http.StatusMethodNotAllowed)
			return
		}

		// Parse media ID from path /api/session/{id}
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 4 {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}
		mediaID := parts[3]
		jobID := "job-" + mediaID

		log.Printf("[Session] Explicit stop request for media %s (Job %s)", mediaID, jobID)

		if err := jobQueue.DeleteJob(r.Context(), jobID); err != nil {
			log.Printf("[Session] Failed to stop job %s: %v", jobID, err)
			// Don't error out to client, as they are leaving anyway
		}
		w.WriteHeader(http.StatusOK)
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
	} else {
		// Job exists, touch it
		queue.TouchJob(r.Context(), jobID)
	}

	// Poll Status (Wait for at least one segment or Ready status)
	timeout := time.After(30 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for job.Status != "Ready" && len(job.Segments) == 0 {
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

func serveDynamicPlaylist(w http.ResponseWriter, job *domain.TranscodeJob) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	// Sort segments by SequenceID to ensure correct ordering
	// This is critical when segments from multiple workers are mixed
	sortedSegments := make([]domain.Segment, len(job.Segments))
	copy(sortedSegments, job.Segments)
	sort.Slice(sortedSegments, func(i, j int) bool {
		return sortedSegments[i].SequenceID < sortedSegments[j].SequenceID
	})

	// Calculate max duration for TARGETDURATION
	maxDuration := 5.0
	for _, seg := range sortedSegments {
		if seg.Duration > maxDuration {
			maxDuration = seg.Duration
		}
	}

	// Basic HLS Header
	fmt.Fprintf(w, "#EXTM3U\n")
	fmt.Fprintf(w, "#EXT-X-VERSION:6\n") // Version 6 for better discontinuity support
	fmt.Fprintf(w, "#EXT-X-TARGETDURATION:%d\n", int(maxDuration)+1)
	firstSeqID := 0
	if len(sortedSegments) > 0 {
		firstSeqID = sortedSegments[0].SequenceID
	}
	fmt.Fprintf(w, "#EXT-X-MEDIA-SEQUENCE:%d\n", firstSeqID)
	fmt.Fprintf(w, "#EXT-X-PLAYLIST-TYPE:EVENT\n")
	fmt.Fprintf(w, "#EXT-X-INDEPENDENT-SEGMENTS\n") // Each segment can be decoded independently

	// List all known segments with discontinuity markers
	var lastWorkerID string
	discontinuityCount := 0
	for i, seg := range sortedSegments {
		// Check for discontinuity (Worker changed)
		if i > 0 && seg.WorkerID != lastWorkerID && lastWorkerID != "" {
			fmt.Fprintf(w, "#EXT-X-DISCONTINUITY\n")
			log.Printf("[Playlist] Discontinuity #%d injected between segment %d (worker %s) and segment %d (worker %s)",
				discontinuityCount+1, sortedSegments[i-1].SequenceID, lastWorkerID, seg.SequenceID, seg.WorkerID)
			discontinuityCount++
		}
		lastWorkerID = seg.WorkerID

		fmt.Fprintf(w, "#EXTINF:%.6f,\n", seg.Duration)
		fmt.Fprintf(w, "segment_%03d.ts\n", seg.SequenceID)
	}

	// If job is done (not implemented yet), add ENDLIST.
	// For now, we assume it's always live/growing until we have a "Completed" status.
	if job.Status == "Completed" { // We don't have this status yet
		fmt.Fprintf(w, "#EXT-X-ENDLIST\n")
	}

	log.Printf("[Playlist] Generated dynamic playlist for job %s: %d segments (Range: %d-%d), Discontinuities: %d",
		job.ID, len(sortedSegments),
		func() int {
			if len(sortedSegments) > 0 {
				return sortedSegments[0].SequenceID
			}
			return 0
		}(),
		func() int {
			if len(sortedSegments) > 0 {
				return sortedSegments[len(sortedSegments)-1].SequenceID
			}
			return 0
		}(),
		discontinuityCount)
}

func calculateStartTime(job *domain.TranscodeJob, startSeg int) float64 {
	// Try to find the immediately preceding segment
	for _, s := range job.Segments {
		if s.SequenceID == startSeg-1 {
			return s.Timestamp + s.Duration
		}
	}

	// Fallback: Iterate to find the highest sequence ID < startSeg
	var bestSeg *domain.Segment
	for i := range job.Segments {
		s := &job.Segments[i]
		if s.SequenceID < startSeg {
			if bestSeg == nil || s.SequenceID > bestSeg.SequenceID {
				bestSeg = s
			}
		}
	}

	if bestSeg != nil {
		// Estimate forward from the best segment we have
		// Assuming 4s for missing segments in between (if any)
		missingCount := startSeg - bestSeg.SequenceID - 1
		return bestSeg.Timestamp + bestSeg.Duration + float64(missingCount)*4.0
	}

	// Total fallback
	return float64(startSeg) * 4.0
}

func triggerJITRecovery(ctx context.Context, queue ports.JobQueue, job *domain.TranscodeJob, startSeg int, startTime float64) error {
	// Deduplication: Check if recovery is already in progress
	// Prevents "recovery storms" where multiple missing segments trigger multiple recovery attempts
	if job.RecoveryInProgress {
		timeSinceLastRecovery := time.Since(job.LastRecoveryTime)
		if timeSinceLastRecovery < 5*time.Second {
			log.Printf("[Recovery] SKIPPED: Recovery already in progress for job %s (triggered %.1fs ago)",
				job.ID, timeSinceLastRecovery.Seconds())
			return nil
		}
		// If it's been more than 5 seconds, the previous recovery might have failed, allow retry
		log.Printf("[Recovery] Previous recovery timeout (%.1fs ago), allowing new recovery attempt",
			timeSinceLastRecovery.Seconds())
	}

	// Purge any stale pending tasks for this job from the queue to prevent workers
	// from picking up obsolete recovery instructions (e.g. recovering an older segment)
	if err := queue.PurgeJobs(ctx, job.ID); err != nil {
		log.Printf("[Recovery] Warning: Failed to purge stale jobs for %s: %v", job.ID, err)
	}

	// 1. Mark job as recovering/pending
	job.Status = "Pending"
	job.RestartCount++
	job.StartSegment = startSeg
	job.StartTime = startTime
	job.RecoveryInProgress = true
	job.LastRecoveryTime = time.Now()

	// 2. Enqueue it again (Compute Plane will pick it up)
	// Note: In a real system, we might need to explicitly kill the old worker if it's still running but stuck.
	// Here, we assume the old worker is dead or we just start a new one.
	// The queue implementation handles deduplication or updates.

	log.Printf("[Control] Re-queuing job %s for JIT recovery at segment %d", job.ID, startSeg)
	return queue.Enqueue(ctx, job)
}

// startPassiveRecoveryMonitor checks for dead jobs and triggers JIT recovery
// This replaces the old "Reaper" service with a smarter, state-aware recovery mechanism.
func startSessionWatchdog(ctx context.Context, queue ports.JobQueue) {
	log.Println("Starting Session Watchdog...")
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			jobs, err := queue.ListActiveJobs(ctx)
			if err != nil {
				log.Printf("[Watchdog] Failed to list jobs: %v", err)
				continue
			}

			now := time.Now()
			// Timeout after 60 seconds of inactivity
			threshold := 60 * time.Second

			for _, job := range jobs {
				// Check LastAccessedAt
				if !job.LastAccessedAt.IsZero() && now.Sub(job.LastAccessedAt) > threshold {
					log.Printf("[Watchdog] 🛑 Job %s IDLE TIMEOUT (Last accessed: %s ago). Deleting...",
						job.ID, now.Sub(job.LastAccessedAt))

					// Delete the job (Worker will detect "Job Not Found" or Update error and exit)
					if err := queue.DeleteJob(ctx, job.ID); err != nil {
						log.Printf("[Watchdog] Failed to delete job %s: %v", job.ID, err)
					}
				}
			}
		}
	}
}

// startPassiveRecoveryMonitor checks for dead jobs and triggers JIT recovery
// This replaces the old "Reaper" service with a smarter, state-aware recovery mechanism.
func startPassiveRecoveryMonitor(ctx context.Context, queue ports.JobQueue) {
	log.Println("Starting Passive Recovery Monitor (JIT-Aware)...")
	ticker := time.NewTicker(1 * time.Second) // Check every second for fast recovery
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			jobs, err := queue.ListActiveJobs(ctx)
			if err != nil {
				log.Printf("Recovery Monitor failed to list jobs: %v", err)
				continue
			}

			now := time.Now()
			threshold := 3 * time.Second // Fast Recovery: Allow 3 missed heartbeats (3s)

			for _, job := range jobs {
				// Skip Completed jobs (they don't need heartbeats)
				if job.Status == "Completed" {
					continue
				}

				if !job.LastHeartbeat.IsZero() && now.Sub(job.LastHeartbeat) > threshold {
					log.Printf("[Recovery] ⚠️ DETECTED DEAD WORKER (ID: %s) for Job %s. Last heartbeat: %s ago.",
						job.WorkerID, job.ID, now.Sub(job.LastHeartbeat))

					// CRITICAL: Mark the dead worker's segments as invalid
					// This prevents the proxy from trying to serve them from the dead worker
					// and ensures discontinuity markers are properly placed
					lastKnownSeg, err := queue.MarkWorkerAsDead(ctx, job.ID, job.WorkerID)
					if err != nil {
						log.Printf("[Recovery] Failed to mark worker %s as dead: %v", job.WorkerID, err)
					} else {
						log.Printf("[Recovery] Marked worker %s as dead, removed its segments from job %s",
							job.WorkerID, job.ID)
					}

					// Determine resume point from last reported segment
					startSeg := 0
					startTime := 0.0

					// Use lastKnownSeg from MarkWorkerAsDead if available, otherwise use job.LastSegmentNum
					if lastKnownSeg > 0 {
						startSeg = lastKnownSeg + 1
						log.Printf("[Recovery] Found existing segments (Last: %d). Resuming from Segment %d",
							lastKnownSeg, startSeg)
					} else if job.LastSegmentNum > 0 {
						startSeg = job.LastSegmentNum + 1
						log.Printf("[Recovery] Using LastSegmentNum: %d. Resuming from Segment %d",
							job.LastSegmentNum, startSeg)
					} else {
						log.Printf("[Recovery] No existing segments found. Restarting from beginning.")
					}

					// Estimate start time based on segment number or use TranscodedDuration if available
					if job.TranscodedDuration > 0 {
						startTime = job.TranscodedDuration
					} else {
						startTime = calculateStartTime(job, startSeg)
					}

					// Trigger JIT Recovery
					if err := triggerJITRecovery(ctx, queue, job, startSeg, startTime); err != nil {
						log.Printf("[Recovery] CRITICAL: Failed to recover job %s: %v", job.ID, err)
					}
				}
			}
		}
	}
}
