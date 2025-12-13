package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/yaffw/yaffw/src/internal/adapters/http_queue"
	"github.com/yaffw/yaffw/src/internal/services"
)

func main() {
	log.Println("Starting yaffw Compute Plane Worker...")

	// 1. Setup Connection to Control Plane (Mock Queue)
	controlURL := os.Getenv("CONTROL_URL")
	if controlURL == "" {
		controlURL = "http://localhost:8096"
	}

	queueClient := http_queue.NewHTTPClientQueue(controlURL + "/internal/queue")

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
	log.Printf("Worker %s connecting to Queue at %s", workerID, controlURL)
	worker.Start(context.Background())
}
