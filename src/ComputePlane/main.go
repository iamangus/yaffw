package main

import (
	"context"
	"fmt"
	"log"
	"os"
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
		cfg.WorkerPort = os.Getenv("WORKER_PORT")
		if cfg.WorkerPort == "" {
			cfg.WorkerPort = "8080"
		}
	} else {
		log.Printf("Loaded config from %s", configFile)
	}

	// Set env vars for internal services that rely on them (like worker.go)
	if cfg.WorkerPort != "" {
		os.Setenv("WORKER_PORT", cfg.WorkerPort)
	}

	// 1. Setup Connection to Control Plane (Mock Queue)
	queueClient := http_queue.NewHTTPClientQueue(cfg.ControlURL + "/internal/queue")

	// 2. Start Worker
	// Use a unique ID per run to detect restarts/crashes in the Control Plane
	// In a real K8s env, this would be the Pod Name.
	hostname, _ := os.Hostname()
	workerID := fmt.Sprintf("%s-%d", hostname, time.Now().UnixNano())
	if hostname == "" {
		workerID = fmt.Sprintf("compute-node-%d", time.Now().UnixNano())
	}

	worker := services.NewTranscodeWorker(queueClient, workerID)

	// 3. Block
	log.Printf("Worker %s connecting to Queue at %s", workerID, cfg.ControlURL)
	worker.Start(context.Background())
}
