package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yaffw/yaffw/src/internal/adapters/filesystem"
	"github.com/yaffw/yaffw/src/internal/adapters/http_queue"
	"github.com/yaffw/yaffw/src/internal/adapters/nfo"
	omniarr_adapter "github.com/yaffw/yaffw/src/internal/adapters/omniarr"
	"github.com/yaffw/yaffw/src/internal/adapters/postgres"
	"github.com/yaffw/yaffw/src/internal/config"
	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
	"github.com/yaffw/yaffw/src/internal/services"
)

var playbackSvc *services.PlaybackService
var transcodeCfg config.TranscodeConfig
var segmentDir string // shared FS path (NFS) where worker writes .ts files and CP serves them
var splitter *services.Splitter
var pgJobQueue *postgres.PostgresJobQueue
var mediaStore *services.MediaStore
var metadataProviders map[string]ports.MetadataProvider
var omniarrConfigs map[string]config.LibraryConfig
var scanners map[string]*filesystem.Scanner

func main() {
	log.Println("Starting yaffw Control Plane...")

	configFile := "config.yaml"
	if len(os.Args) > 1 {
		configFile = os.Args[1]
	}

	var cfg config.ControlPlaneConfig
	if err := config.Load(configFile, &cfg); err != nil {
		log.Printf("Warning: Could not load config file '%s': %v. Using defaults/env.", configFile, err)
		cfg.DatabaseURL = os.Getenv("DATABASE_URL")
		if cfg.DatabaseURL == "" {
			cfg.DatabaseURL = "postgres://user:password@localhost:5432/yaffw?sslmode=disable"
		}
		cfg.Port = os.Getenv("PORT")
		if cfg.Port == "" {
			cfg.Port = "8096"
		}
		cfg.FrontendAPIKey = os.Getenv("FRONTEND_API_KEY")
	} else {
		log.Printf("Loaded configuration from %s", configFile)
	}

	if len(cfg.Libraries) == 0 {
		log.Println("Warning: No libraries configured.")
	}

	db, err := postgres.NewConnection(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to Postgres: %v", err)
	}

	identityRepo := postgres.NewIdentityRepo(db)
	if err := identityRepo.InitSchema(context.Background()); err != nil {
		log.Fatalf("Failed to init identity schema: %v", err)
	}

	pgJobQueue = postgres.NewJobQueue(db)
	if err := pgJobQueue.InitSchema(); err != nil {
		log.Fatalf("Failed to init queue schema: %v", err)
	}
	var jobQueue ports.JobQueue = pgJobQueue

	transcodeCfg = cfg.Transcode.WithDefaults()
	segmentDir = cfg.Transcode.SegmentDir
	if v := os.Getenv("SEGMENT_DIR"); v != "" {
		segmentDir = v
	}
	if err := os.MkdirAll(segmentDir, 0755); err != nil {
		log.Fatalf("Failed to create segmentDir %s: %v", segmentDir, err)
	}
	splitter = services.NewSplitter(jobQueue, "ffprobe")

	mediaStore = services.NewMediaStore()
	metadataProviders = buildMetadataProviders(cfg.Libraries)
	omniarrConfigs = make(map[string]config.LibraryConfig)
	scanners = make(map[string]*filesystem.Scanner)

	for _, lib := range cfg.Libraries {
		provider := metadataProviders[lib.Name]
		scanner := filesystem.NewScanner(lib, provider, identityRepo, mediaStore)
		scanners[lib.Name] = scanner

		if lib.MetadataProvider == "omniarr" {
			omniarrConfigs[lib.Name] = lib
		}

		go func(l config.LibraryConfig, s *filesystem.Scanner) {
			log.Printf("[Scanner] Starting initial scan for library '%s' at %s", l.Name, l.Path)
			if err := s.Scan(context.Background()); err != nil {
				log.Printf("[Scanner] Initial scan failed for '%s': %v", l.Name, err)
			}

			if l.ScanInterval != "" {
				interval, err := time.ParseDuration(l.ScanInterval)
				if err != nil || interval == 0 {
					interval = 10 * time.Minute
				}
				go startPeriodicScan(context.Background(), l.Name, s, interval)
			}

			if l.WatchFS {
				watcher, err := filesystem.NewWatcher(l.Path, s)
				if err != nil {
					log.Printf("[Watcher] Failed to start for '%s': %v", l.Name, err)
				} else {
					watcher.Start()
					log.Printf("[Watcher] Started for library '%s'", l.Name)
				}
			}
		}(lib, scanner)
	}

	log.Printf("Initialized %d metadata providers, %d libraries", len(metadataProviders), len(cfg.Libraries))

	ffmpegPath := "ffmpeg"
	playbackSvc = services.NewPlaybackService(ffmpegPath)

	go startPassiveRecoveryMonitor(context.Background(), jobQueue)
	go startSessionWatchdog(context.Background(), jobQueue)

	simpleAuth := NewSimpleAuthMiddleware(cfg.FrontendAPIKey)

	mux := http.NewServeMux()
	queueServer := http_queue.NewHTTPServerQueue(jobQueue, transcodeCfg.InternalToken)
	queueServer.RegisterHandlers(mux)
	log.Println("Exposing internal queue at /internal/queue/...")

	mux.Handle("/metrics", services.GlobalMetrics)

	mux.HandleFunc("/images/", handleImageProxy)

	mux.HandleFunc("/local-images/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/local-images/"), "/", 2)
		if len(parts) != 2 {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		itemID := parts[0]
		filename := parts[1]

		if filename == "" || strings.ContainsAny(filename, "/\\") || filename == ".." || strings.Contains(filename, "..") {
			http.Error(w, "Invalid filename", http.StatusBadRequest)
			return
		}

		item, ok := mediaStore.GetByID(itemID)
		if !ok || item.Path == "" {
			http.Error(w, "Media not found", http.StatusNotFound)
			return
		}

		dir := filepath.Dir(item.Path)
		fullPath := filepath.Join(dir, filename)

		absDir, errDir := filepath.Abs(dir)
		absFull, errFull := filepath.Abs(fullPath)
		if errDir != nil || errFull != nil {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(absFull, absDir+string(filepath.Separator)) {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}

		w.Header().Set("Cache-Control", "public, max-age=3600")
		http.ServeFile(w, r, fullPath)
	})

	mux.Handle("/api/v1/libraries", simpleAuth.RequireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg.Libraries)
	})))

	mux.Handle("/api/v1/libraries/", simpleAuth.RequireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/libraries/"), "/")
		if len(parts) < 2 || parts[1] != "scan" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		libName := parts[0]
		scanner, ok := scanners[libName]
		if !ok {
			http.Error(w, "Library not found", http.StatusNotFound)
			return
		}
		go func() {
			if err := scanner.Scan(context.Background()); err != nil {
				log.Printf("[Scanner] Manual scan failed for '%s': %v", libName, err)
			}
		}()
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"status": "scanning"})
	})))

	mux.Handle("/api/v1/media", simpleAuth.RequireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		libraryID := r.URL.Query().Get("libraryId")
		parentID := r.URL.Query().Get("parentId")

		var items []*domain.MediaItem
		if parentID != "" {
			items = mediaStore.GetChildren(parentID)
		} else if libraryID != "" {
			items = mediaStore.ListByLibrary(libraryID)
		} else {
			items = mediaStore.ListAll()
		}

		log.Printf("[API] ListAll returning %d items (Library: %s, Parent: %s)", len(items), libraryID, parentID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)
	})))

	mux.Handle("/api/v1/media/", simpleAuth.RequireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		id := strings.TrimPrefix(r.URL.Path, "/api/v1/media/")
		if id == "" {
			http.Error(w, "ID required", http.StatusBadRequest)
			return
		}

		item, ok := mediaStore.GetByID(id)
		if !ok {
			http.Error(w, "Media not found", http.StatusNotFound)
			return
		}

		metadata := fetchMetadataForItem(item)
		item.Metadata = metadata

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(item)
	})))

	mux.Handle("/hls/", http.StripPrefix("/hls/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 2 {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}
		jobID := parts[0]
		filename := parts[1]

		if strings.HasSuffix(filename, ".m3u8") {
			job, err := jobQueue.GetJob(r.Context(), jobID)
			if err != nil {
				http.Error(w, "Job not found", http.StatusNotFound)
				return
			}
			if job.Status == "Failed" {
				http.Error(w, "Transcode failed: "+job.FailureReason, http.StatusInternalServerError)
				return
			}
			if err := jobQueue.TouchJob(r.Context(), jobID); err != nil {
				log.Printf("[Control] TouchJob failed for job %s on playlist: %v", jobID, err)
			}
			serveDynamicPlaylist(w, job)
			return
		}

		if strings.HasSuffix(filename, ".ts") {
			numStr := strings.TrimSuffix(strings.TrimPrefix(filename, "segment_"), ".ts")
			segNum, err := strconv.Atoi(numStr)
			if err != nil {
				http.Error(w, "Invalid segment name", http.StatusBadRequest)
				return
			}

			if err := jobQueue.TouchJob(r.Context(), jobID); err != nil {
				log.Printf("[Control] TouchJob failed for job %s on segment: %v", jobID, err)
			}

			seg, err := jobQueue.GetSegment(r.Context(), jobID, segNum)
			if err == nil && seg != nil {
				segPath := filepath.Join(segmentDir, jobID, filename)
				if _, statErr := os.Stat(segPath); statErr == nil {
					log.Printf("[Control] Serving segment %d for job %s from %s", segNum, jobID, segPath)
					http.ServeFile(w, r, segPath)
					return
				} else if !os.IsNotExist(statErr) {
					log.Printf("[Control] WARNING: os.Stat failed for segment %d path %s: %v", segNum, segPath, statErr)
				}
				log.Printf("[Control] Segment %d reported (DB row exists) but file missing on disk for job %s. Falling through to JIT.", segNum, jobID)
			}

			log.Printf("[Control] Segment %d missing for job %s. Triggering JIT Re-transcode.", segNum, jobID)
			job, err := jobQueue.GetJob(r.Context(), jobID)
			if err != nil {
				http.Error(w, "Job not found", http.StatusNotFound)
				return
			}
			if job.Status == "Failed" {
				http.Error(w, "Transcode failed: "+job.FailureReason, http.StatusInternalServerError)
				return
			}

			startTime := calculateStartTime(job, segNum)
			if err := triggerJITRecovery(r.Context(), jobQueue, job, segNum, startTime); err != nil {
				log.Printf("[Control] Failed to trigger recovery for job %s segment %d: %v", jobID, segNum, err)
				http.Error(w, "Recovery failed", http.StatusInternalServerError)
				return
			}

			w.Header().Set("Retry-After", "2")
			http.Error(w, "Segment regenerating...", http.StatusServiceUnavailable)
			return
		}

		http.Error(w, "Not found", http.StatusNotFound)
	})))

	mux.Handle("/api/playback/", simpleAuth.RequireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlePlaybackRequest(w, r, jobQueue)
	})))

	mux.HandleFunc("/api/session/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/session/"), "/")
		if len(parts) < 1 {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}
		mediaID := parts[0]
		jobID := "job-" + mediaID

		if len(parts) > 1 && parts[1] == "seek" {
			if r.Method != http.MethodPost {
				http.Error(w, "POST required", http.StatusMethodNotAllowed)
				return
			}
			var seekReq struct {
				Position float64 `json:"position"`
			}
			if err := json.NewDecoder(r.Body).Decode(&seekReq); err != nil {
				http.Error(w, "Invalid JSON", http.StatusBadRequest)
				return
			}

			log.Printf("[Session] Seek request for media %s to position %.2fs", mediaID, seekReq.Position)

			if err := jobQueue.DeleteJob(r.Context(), jobID); err != nil {
				log.Printf("[Session] Failed to delete old job %s for seek: %v", jobID, err)
			}

			startSeg := int(seekReq.Position / float64(transcodeCfg.HLSSegmentTime))
			newJob := &domain.TranscodeJob{
				ID:           jobID,
				MediaID:      mediaID,
				FilePath:     "",
				Status:       "Pending",
				StartSegment: startSeg,
				StartTime:    seekReq.Position,
			}
			if oldJob, err := jobQueue.GetJob(r.Context(), jobID); err == nil {
				newJob.FilePath = oldJob.FilePath
				newJob.TargetVideoCodec = oldJob.TargetVideoCodec
				newJob.TargetAudioCodec = oldJob.TargetAudioCodec
				newJob.TargetContainer = oldJob.TargetContainer
				newJob.NeedsTranscode = oldJob.NeedsTranscode
			}
			if newJob.FilePath == "" {
				item, ok := mediaStore.GetByID(mediaID)
				if !ok {
					http.Error(w, "Media not found", http.StatusNotFound)
					return
				}
				newJob.FilePath = item.Path
				newJob.TargetVideoCodec = "libx264"
				newJob.TargetAudioCodec = "aac"
				newJob.TargetContainer = "hls"
				newJob.NeedsTranscode = true
			}

			if err := jobQueue.Enqueue(r.Context(), newJob); err != nil {
				log.Printf("[Session] Failed to enqueue seek job %s: %v", jobID, err)
				http.Error(w, "Failed to start seek transcode", http.StatusInternalServerError)
				return
			}
			log.Printf("[Session] Enqueued seek job %s (startSeg=%d, startTime=%.2fs)", jobID, startSeg, seekReq.Position)
			w.WriteHeader(http.StatusAccepted)
			return
		}

		if r.Method != http.MethodDelete {
			http.Error(w, "DELETE required", http.StatusMethodNotAllowed)
			return
		}
		log.Printf("[Session] Explicit stop request for media %s (Job %s)", mediaID, jobID)
		if err := jobQueue.DeleteJob(r.Context(), jobID); err != nil {
			log.Printf("[Session] Failed to delete job %s for stop: %v", jobID, err)
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/stream/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/stream/"), "/master.m3u8")
			jobID := "job-" + id
			http.Redirect(w, r, fmt.Sprintf("/hls/%s/stream.m3u8", jobID), http.StatusFound)
			return
		}

		id := r.URL.Path[len("/stream/"):]
		item, ok := mediaStore.GetByID(id)
		if !ok {
			http.Error(w, "Media not found", http.StatusNotFound)
			return
		}
		log.Printf("Direct Streaming %s", item.Title)
		http.ServeFile(w, r, item.Path)
	})

	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Pong"))
	})

	mux.HandleFunc("/client-log", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			return
		}
		var msg map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			log.Printf("[ClientLog] Failed to decode client log message: %v", err)
			return
		}
		log.Printf("[CLIENT] %s: %v", msg["level"], msg["message"])
	})

	log.Printf("Control Plane listening on http://0.0.0.0:%s", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, mux); err != nil {
		log.Fatal(err)
	}
}

func handleImageProxy(w http.ResponseWriter, r *http.Request) {
	pathParts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/images/"), "/", 2)
	if len(pathParts) != 2 {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	libraryName := pathParts[0]
	filename := pathParts[1]

	lib, ok := omniarrConfigs[libraryName]
	if !ok {
		http.Error(w, "Library not found", http.StatusNotFound)
		return
	}

	omniarrImageURL := fmt.Sprintf("%s/images/%s", lib.OmniarrURL, filename)

	req, err := http.NewRequest("GET", omniarrImageURL, nil)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("X-Api-Key", lib.OmniarrAPIKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Failed to fetch image from omniarr: %v", err)
		http.Error(w, "Image not found", http.StatusNotFound)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Cache-Control", "public, max-age=3600")
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("[Images] Failed to proxy image bytes: %v", err)
	}
}

func startPeriodicScan(ctx context.Context, libName string, scanner *filesystem.Scanner, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			log.Printf("[Scanner] Periodic scan for library '%s'", libName)
			if err := scanner.Scan(ctx); err != nil {
				log.Printf("[Scanner] Periodic scan failed for '%s': %v", libName, err)
			}
		}
	}
}

type PlaybackRequest struct {
	MediaID      string                    `json:"mediaId"`
	Capabilities domain.ClientCapabilities `json:"capabilities"`
}

type PlaybackResponse struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	MediaType string `json:"mediaType"`
}

func handlePlaybackRequest(w http.ResponseWriter, r *http.Request, queue ports.JobQueue) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	var req PlaybackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	item, ok := mediaStore.GetByID(req.MediaID)
	if !ok {
		http.Error(w, "Media not found", http.StatusNotFound)
		return
	}

	jobID := "job-" + req.MediaID
	if existingJob, err := queue.GetJob(r.Context(), jobID); err == nil {
		if existingJob.Status == "Ready" || existingJob.Status == "Processing" || existingJob.Status == "Completed" {
			log.Printf("Active job %s found (Status: %s). Skipping probe.", jobID, existingJob.Status)
			resp := PlaybackResponse{
				Type:      "transcode",
				URL:       fmt.Sprintf("/hls/%s/stream.m3u8", jobID),
				MediaType: "application/vnd.apple.mpegurl",
			}
			if existingJob.StreamURL != "" {
				resp.URL = existingJob.StreamURL
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
	}

	var probe *domain.MediaProbe
	if pgJobQueue != nil {
		if cached, err := pgJobQueue.GetCachedProbe(r.Context(), req.MediaID); err == nil {
			probe = cached
			log.Printf("Using cached probe for %s", req.MediaID)
		}
	}
	if probe == nil {
		var err error
		probe, err = playbackSvc.ProbeMedia(r.Context(), item.Path)
		if err != nil {
			log.Printf("Failed to probe %s: %v", item.Path, err)
			resp := PlaybackResponse{
				Type:      "transcode",
				URL:       fmt.Sprintf("/hls/%s/stream.m3u8", jobID),
				MediaType: "application/vnd.apple.mpegurl",
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		if pgJobQueue != nil {
			pgJobQueue.CacheProbe(r.Context(), req.MediaID, probe)
		}
	}

	strategy := playbackSvc.DeterminePlaybackStrategy(probe, &req.Capabilities)

	if !strategy.NeedsTranscode {
		log.Printf("Direct Play for %s (video=%s, audio=%s)", item.Title, probe.VideoCodec, probe.AudioCodec)
		resp := PlaybackResponse{
			Type:      "direct",
			URL:       "/stream/" + req.MediaID,
			MediaType: "video/" + probe.Container,
		}
		json.NewEncoder(w).Encode(resp)
		return
	}

	existingJob, err := queue.GetJob(r.Context(), jobID)
	if err != nil {
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

		if transcodeCfg.ParallelRanges > 1 && splitter != nil {
			_, numSubJobs, splitErr := splitter.SplitAndEnqueue(r.Context(), newJob, transcodeCfg.ParallelRanges, float64(transcodeCfg.HLSSegmentTime))
			if splitErr != nil {
				log.Printf("Splitter failed, falling back to single job: %v", splitErr)
				queue.Enqueue(r.Context(), newJob)
			} else {
				log.Printf("Split job %s into %d parallel sub-jobs", jobID, numSubJobs)
			}
		} else {
			queue.Enqueue(r.Context(), newJob)
		}
		existingJob = newJob
	}

	resp := PlaybackResponse{
		Type:      "transcode",
		URL:       fmt.Sprintf("/hls/%s/stream.m3u8", jobID),
		MediaType: "application/vnd.apple.mpegurl",
	}

	if existingJob.Status == "Ready" && existingJob.StreamURL != "" {
		resp.URL = existingJob.StreamURL
	}

	json.NewEncoder(w).Encode(resp)
}

// serveDynamicPlaylist generates an HLS .m3u8 playlist from the job's segments in the DB.
// It injects #EXT-X-DISCONTINUITY tags whenever the WorkerID changes between adjacent
// segments. Worker changes still matter under the shared-FS model because each worker
// runs its own ffmpeg instance with independent GOP/keyframe alignment — a discontinuity
// tag tells the player to reset its decoder state across the worker boundary.
func serveDynamicPlaylist(w http.ResponseWriter, job *domain.TranscodeJob) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	sortedSegments := make([]domain.Segment, len(job.Segments))
	copy(sortedSegments, job.Segments)
	sort.Slice(sortedSegments, func(i, j int) bool {
		return sortedSegments[i].SequenceID < sortedSegments[j].SequenceID
	})

	maxDuration := 5.0
	for _, seg := range sortedSegments {
		if seg.Duration > maxDuration {
			maxDuration = seg.Duration
		}
	}

	fmt.Fprintf(w, "#EXTM3U\n")
	fmt.Fprintf(w, "#EXT-X-VERSION:6\n")
	fmt.Fprintf(w, "#EXT-X-TARGETDURATION:%d\n", int(maxDuration)+1)
	firstSeqID := 0
	if len(sortedSegments) > 0 {
		firstSeqID = sortedSegments[0].SequenceID
	}
	fmt.Fprintf(w, "#EXT-X-MEDIA-SEQUENCE:%d\n", firstSeqID)
	if job.DiscontinuityCount > 0 {
		fmt.Fprintf(w, "#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", job.DiscontinuityCount)
	}
	fmt.Fprintf(w, "#EXT-X-INDEPENDENT-SEGMENTS\n")

	var lastWorkerID string
	discontinuityCount := 0
	for i, seg := range sortedSegments {
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

	if job.Status == "Completed" {
		fmt.Fprintf(w, "#EXT-X-ENDLIST\n")
	}

	log.Printf("[Playlist] Generated dynamic playlist for job %s: %d segments, Discontinuities: %d",
		job.ID, len(sortedSegments), discontinuityCount)
}

// highestSegmentOnDisk returns the sequence ID of the highest-numbered segment_NNN.ts file
// present on the shared filesystem for a given job, or -1 if none exist.
// filepath.Glob's only error is a malformed pattern; the pattern here is hardcoded and safe,
// so the error is intentionally ignored.
func highestSegmentOnDisk(segmentDir, jobID string) int {
	matches, _ := filepath.Glob(filepath.Join(segmentDir, jobID, "segment_*.ts"))
	max := -1
	for _, m := range matches {
		base := filepath.Base(m)
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(base, "segment_"), ".ts"))
		if err == nil && n > max {
			max = n
		}
	}
	return max
}

// calculateStartTime estimates the media timestamp for a given segment number.
// It first looks for an exact match (segment startSeg-1) to compute the precise offset,
// then falls back to the closest available segment below startSeg, and finally to an
// arithmetic estimate based on the HLS segment time.
func calculateStartTime(job *domain.TranscodeJob, startSeg int) float64 {
	for _, s := range job.Segments {
		if s.SequenceID == startSeg-1 {
			return s.Timestamp + s.Duration
		}
	}

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
		missingCount := startSeg - bestSeg.SequenceID - 1
		return bestSeg.Timestamp + bestSeg.Duration + float64(missingCount)*float64(transcodeCfg.HLSSegmentTime)
	}

	return float64(startSeg) * float64(transcodeCfg.HLSSegmentTime)
}

// triggerJITRecovery re-enqueues a job for just-in-time segment recovery.
// It sets the job to Pending and increments RestartCount. If the job is already
// being recovered (RecoveryInProgress), the sentinel error is swallowed and nil
// is returned — this is intentional de-duplication to avoid double-enqueue races.
func triggerJITRecovery(ctx context.Context, queue ports.JobQueue, job *domain.TranscodeJob, startSeg int, startTime float64) error {
	job.Status = "Pending"
	job.RestartCount++
	job.StartSegment = startSeg
	job.StartTime = startTime
	job.RecoveryInProgress = true
	job.LastRecoveryTime = time.Now()

	log.Printf("[Control] Re-queuing job %s for JIT recovery at segment %d", job.ID, startSeg)
	if err := queue.Enqueue(ctx, job); err != nil {
		if errors.Is(err, domain.ErrRecoveryInProgress) {
			log.Printf("[Recovery] SKIPPED: Recovery already in progress for job %s", job.ID)
			return nil
		}
		return err
	}
	return nil
}

// startSessionWatchdog periodically reaps idle and completed jobs past their retention.
// On reaping, it deletes both the DB row and the segment directory on the shared FS.
// Note: if the process dies between DeleteJob and RemoveAll, segment files are orphaned
// (DB row gone, files remain). A periodic orphan-cleanup job should be added if this
// becomes a problem in practice.
func startSessionWatchdog(ctx context.Context, queue ports.JobQueue) {
	log.Println("Starting Session Watchdog...")
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	idleThreshold := time.Duration(transcodeCfg.IdleTimeout) * time.Second
	completedRetention := time.Duration(transcodeCfg.CompletedRetention) * time.Second

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
			for _, job := range jobs {
				if job.LastAccessedAt.IsZero() {
					continue
				}
				idle := now.Sub(job.LastAccessedAt)
				threshold := idleThreshold
				if job.Status == "Completed" {
					threshold = completedRetention
				}
				if idle > threshold {
					log.Printf("[Watchdog] Job %s IDLE TIMEOUT (Status: %s, Last accessed: %s ago). Deleting...",
						job.ID, job.Status, idle)
					if err := queue.DeleteJob(ctx, job.ID); err != nil {
						log.Printf("[Watchdog] Failed to delete job %s: %v", job.ID, err)
					}
					if err := os.RemoveAll(filepath.Join(segmentDir, job.ID)); err != nil {
						log.Printf("[Watchdog] Failed to clean segment dir for job %s: %v", job.ID, err)
					}
				}
			}
		}
	}
}

// startPassiveRecoveryMonitor runs a 1s tick that:
//   - detects dead workers (heartbeat timeout) and triggers JIT recovery from the
//     latest segment file on disk (shared-FS model preserves segments across worker deaths)
//   - retries Failed jobs up to MaxRestarts, then permanently cleans them up
//
// This is distinct from the JIT recovery triggered by the segment handler on a client
// .ts request — this monitor catches dead workers proactively via heartbeat.
func startPassiveRecoveryMonitor(ctx context.Context, queue ports.JobQueue) {
	log.Println("[Recovery] Starting Passive Recovery Monitor (heartbeat-based dead-worker detection + failed-job retry)")
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	threshold := time.Duration(transcodeCfg.DeadWorkerThreshold) * time.Second
	maxRestarts := transcodeCfg.MaxRestarts

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			jobs, err := queue.ListActiveJobs(ctx)
			if err != nil {
				log.Printf("[Recovery] Failed to list active jobs: %v", err)
				continue
			}

			now := time.Now()
			for _, job := range jobs {
				if job.Status == "Completed" {
					continue
				}

				if job.Status == "Failed" {
					if job.RestartCount >= maxRestarts {
						log.Printf("[Recovery] Job %s permanently failed after %d restarts. Cleaning up.", job.ID, job.RestartCount)
						if err := queue.DeleteJob(ctx, job.ID); err != nil {
							log.Printf("[Recovery] Failed to delete permanently-failed job %s: %v", job.ID, err)
						}
						if err := os.RemoveAll(filepath.Join(segmentDir, job.ID)); err != nil {
							log.Printf("[Recovery] Failed to clean segment dir for permanently-failed job %s: %v", job.ID, err)
						}
						continue
					}
					log.Printf("[Recovery] Failed job %s (RestartCount: %d). Retrying from segment %d.",
						job.ID, job.RestartCount, job.LastSegmentNum+1)
					startSeg := job.LastSegmentNum + 1
					startTime := job.TranscodedDuration
					if startTime <= 0 {
						startTime = calculateStartTime(job, startSeg)
					}
					if err := triggerJITRecovery(ctx, queue, job, startSeg, startTime); err != nil {
						log.Printf("[Recovery] Failed to retry job %s: %v", job.ID, err)
					}
					continue
				}

				if !job.LastHeartbeat.IsZero() && now.Sub(job.LastHeartbeat) > threshold {
					log.Printf("[Recovery] DETECTED DEAD WORKER (ID: %s) for Job %s. Last heartbeat: %s ago.",
						job.WorkerID, job.ID, now.Sub(job.LastHeartbeat))

					if err := queue.MarkWorkerAsDead(ctx, job.ID, job.WorkerID); err != nil {
						log.Printf("[Recovery] Failed to mark worker %s as dead: %v", job.WorkerID, err)
					} else {
						log.Printf("[Recovery] Marked worker %s as dead for job %s (discontinuity_count incremented)",
							job.WorkerID, job.ID)
					}

					lastOnDisk := highestSegmentOnDisk(segmentDir, job.ID)

					startSeg := 0
					startTime := 0.0

					if lastOnDisk >= 0 {
						startSeg = lastOnDisk + 1
						log.Printf("[Recovery] Found %d segment files on shared FS. Resuming from Segment %d",
							lastOnDisk, startSeg)
					} else if job.LastSegmentNum > 0 {
						startSeg = job.LastSegmentNum + 1
						log.Printf("[Recovery] No files on disk. Using LastSegmentNum: %d. Resuming from Segment %d",
							job.LastSegmentNum, startSeg)
					} else {
						log.Printf("[Recovery] No existing segments found. Restarting from beginning.")
					}

					if job.TranscodedDuration > 0 {
						startTime = job.TranscodedDuration
					} else {
						startTime = calculateStartTime(job, startSeg)
					}

					if err := triggerJITRecovery(ctx, queue, job, startSeg, startTime); err != nil {
						log.Printf("[Recovery] CRITICAL: Failed to recover job %s: %v", job.ID, err)
					}
				}
			}
		}
	}
}

func buildMetadataProviders(libraries []config.LibraryConfig) map[string]ports.MetadataProvider {
	providers := make(map[string]ports.MetadataProvider)
	for _, lib := range libraries {
		providerType := lib.MetadataProvider
		if providerType == "" {
			providerType = "nfo"
		}
		switch providerType {
		case "omniarr":
			if lib.OmniarrURL != "" {
				client := omniarr_adapter.NewOmniarrClient(lib.OmniarrURL, lib.OmniarrAPIKey)
				providers[lib.Name] = omniarr_adapter.NewOmniarrMetadataProvider(client, lib.Name)
			} else {
				log.Printf("[Config] Library '%s' has omniarr provider but no omniarr_url, falling back to nfo", lib.Name)
				providers[lib.Name] = nfo.NewNFOMetadataProvider()
			}
		case "nfo":
			fallthrough
		default:
			providers[lib.Name] = nfo.NewNFOMetadataProvider()
		}
	}
	return providers
}

func fetchMetadataForItem(item *domain.MediaItem) *domain.MediaMetadata {
	provider, ok := metadataProviders[item.LibraryID]
	if !ok {
		return &domain.MediaMetadata{Title: item.Title}
	}
	meta, err := provider.FetchMetadata(context.Background(), item)
	if err != nil {
		log.Printf("[Metadata] Provider %T returned error for %s: %v", provider, item.ID, err)
		return &domain.MediaMetadata{Title: item.Title}
	}
	return meta
}
