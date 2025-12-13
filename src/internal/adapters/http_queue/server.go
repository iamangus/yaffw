package http_queue

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
)

// HTTPServerQueue exposes the In-Memory Queue via HTTP endpoints
// for the separate Compute process to consume.
type HTTPServerQueue struct {
	queue ports.JobQueue
}

func NewHTTPServerQueue(queue ports.JobQueue) *HTTPServerQueue {
	return &HTTPServerQueue{queue: queue}
}

func (h *HTTPServerQueue) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/internal/queue/dequeue", h.handleDequeue)
	mux.HandleFunc("/internal/queue/update", h.handleUpdate)
}

func (h *HTTPServerQueue) handleDequeue(w http.ResponseWriter, r *http.Request) {
	job, err := h.queue.Dequeue(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	json.NewEncoder(w).Encode(job)
}

func (h *HTTPServerQueue) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var job domain.TranscodeJob
	if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Log significant updates to visibility in Control Plane logs
	if job.Status == "Processing" {
		if job.RestartCount > 0 {
			log.Printf("[HTTP Queue] 🔄 WORKER RECOVERY: Worker %s picked up crashed job %s (Restart #%d)",
				job.WorkerID, job.ID, job.RestartCount)
		} else {
			log.Printf("[HTTP Queue] Worker %s started processing job %s", job.WorkerID, job.ID)
		}
	} else if job.Status == "Ready" {
		log.Printf("[HTTP Queue] Worker %s reports job %s is READY (Stream Active)", job.WorkerID, job.ID)
	} else if job.Status == "Failed" {
		log.Printf("[HTTP Queue] ❌ Worker %s reports job %s FAILED", job.WorkerID, job.ID)
	}

	if err := h.queue.UpdateJob(r.Context(), &job); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
