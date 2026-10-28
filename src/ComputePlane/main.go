package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yaffw/yaffw/src/internal/adapters/http_queue"
	"github.com/yaffw/yaffw/src/internal/config"
	"github.com/yaffw/yaffw/src/internal/services"
)

func main() {
	log.Println("Starting yaffw Compute Plane Worker...")

	// Load Config
	configFile := "config.yaml"
	if len(os.Args) > 1 {
		configFile = os.Args[1]
	}

	var cfg config.ComputePlaneConfig
	if err := config.Load(configFile, &cfg); err != nil {
		log.Printf("Warning: Failed to load config '%s': %v. Using defaults/env.", configFile, err)
		cfg.ControlURL = os.Getenv("CONTROL_URL")
		if cfg.ControlURL == "" {
			cfg.ControlURL = "http://localhost:8096"
		}
		cfg.Transcode.InternalToken = os.Getenv("INTERNAL_TOKEN")
	} else {
		log.Printf("Loaded config from %s", configFile)
		// Override with env vars if set (K8s-style)
		if v := os.Getenv("CONTROL_URL"); v != "" {
			cfg.ControlURL = v
		}
		if v := os.Getenv("INTERNAL_TOKEN"); v != "" {
			cfg.Transcode.InternalToken = v
		}
	}

	// Shared segment-directory (NFS); env override wins, then config file.
	segmentDir := os.Getenv("SEGMENT_DIR")
	if segmentDir == "" {
		segmentDir = cfg.Transcode.SegmentDir
	}

	// 1. Setup Connection to Control Plane
	queueClient := http_queue.NewHTTPClientQueue(cfg.ControlURL+"/internal/queue", cfg.Transcode.InternalToken)

	// 2. Start Worker
	hostname, err := os.Hostname()
	if err != nil {
		log.Printf("[Compute] os.Hostname failed: %v; using synthetic worker ID", err)
		hostname = ""
	}
	workerID := fmt.Sprintf("%s-%d", hostname, time.Now().UnixNano())
	if hostname == "" {
		workerID = fmt.Sprintf("compute-node-%d", time.Now().UnixNano())
	}

	worker := services.NewTranscodeWorker(queueClient, workerID, cfg.Transcode, cfg.Encoder, segmentDir)

	// Graceful shutdown on SIGTERM/SIGINT: cancel context, wait for worker.Start
	// to return, with a 10s hard deadline before forced exit.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	log.Printf("Worker %s connecting to Queue at %s (segmentDir: %s)", workerID, cfg.ControlURL, segmentDir)

	// Run worker in a goroutine so the main goroutine can wait for signal + worker done.
	done := make(chan struct{})
	go func() {
		worker.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
		// worker.Start returned on its own (e.g. unrecoverable error)
	case sig := <-sigCh:
		log.Printf("[Compute] Received signal %s. Cancelling worker context and waiting up to 10s...", sig)
		cancel()
		select {
		case <-done:
			log.Printf("[Compute] Worker stopped cleanly. Exiting.")
		case <-time.After(10 * time.Second):
			log.Printf("[Compute] Worker did not stop within 10s. Forcing exit.")
			os.Exit(1)
		}
	}
}
