package services

import (
	"context"
	"log"
	"time"

	"github.com/yaffw/yaffw/src/internal/ports"
)

type JobReaper struct {
	queue ports.JobQueue
}

func NewJobReaper(queue ports.JobQueue) *JobReaper {
	return &JobReaper{queue: queue}
}

// StartMonitoring checks for dead jobs periodically
func (r *JobReaper) StartMonitoring(ctx context.Context) {
	log.Println("Starting Job Reaper (Crash Detection)...")
	ticker := time.NewTicker(2 * time.Second) // Check every 2 seconds
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.checkDeadJobs(ctx)
		}
	}
}

func (r *JobReaper) checkDeadJobs(ctx context.Context) {
	jobs, err := r.queue.ListActiveJobs(ctx)
	if err != nil {
		log.Printf("Reaper failed to list jobs: %v", err)
		return
	}

	now := time.Now()
	threshold := 6 * time.Second // Allow 3 missed heartbeats (2s interval)

	for _, job := range jobs {
		if job.Status == "Processing" || job.Status == "Ready" {
			if !job.LastHeartbeat.IsZero() && now.Sub(job.LastHeartbeat) > threshold {
				log.Printf("[Reaper] ⚠️ DETECTED DEAD WORKER (ID: %s) for Job %s. Last heartbeat: %s ago. Initiating recovery...",
					job.WorkerID, job.ID, now.Sub(job.LastHeartbeat))

				// Recovery Logic
				oldWorkerID := job.WorkerID
				job.Status = "Pending" // Reset to Pending so another worker picks it up
				job.WorkerID = ""
				job.RestartCount++

				if err := r.queue.UpdateJob(ctx, job); err != nil {
					log.Printf("[Reaper] CRITICAL: Failed to recover job %s (Worker: %s): %v", job.ID, oldWorkerID, err)
				} else {
					log.Printf("[Reaper] RECOVERY: Job %s reset to Pending (Restart Count: %d). Re-enqueuing...", job.ID, job.RestartCount)
					// Re-enqueue to make sure it's in the queue list (if implementation requires it)
					// In our InMemory implementation, UpdateJob only updates the map,
					// we need to re-add to the slice if we want Dequeue to see it again.
					// This is a nuance of the memory adapter.
					r.queue.Enqueue(ctx, job)
				}
			}
		}
	}
}
