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
	mux.HandleFunc("/internal/queue/add_segment", h.handleAddSegment)
	mux.HandleFunc("/internal/queue/get_job", h.handleGetJob)
}

func (h *HTTPServerQueue) handleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	job, err := h.queue.GetJob(r.Context(), jobID)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(job)
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
		// Clear recovery flag when worker starts processing
		job.RecoveryInProgress = false
	} else if job.Status == "Ready" {
		log.Printf("[HTTP Queue] Worker %s reports job %s is READY (Stream Active)", job.WorkerID, job.ID)
		// Clear recovery flag when stream becomes ready
		job.RecoveryInProgress = false
	} else if job.Status == "Failed" {
		log.Printf("[HTTP Queue] ❌ Worker %s reports job %s FAILED", job.WorkerID, job.ID)
		job.RecoveryInProgress = false
	} else if job.Status == "Completed" {
		log.Printf("[HTTP Queue] ✅ Worker %s reports job %s COMPLETED", job.WorkerID, job.ID)
		job.RecoveryInProgress = false
	}

	if err := h.queue.UpdateJob(r.Context(), &job); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *HTTPServerQueue) handleAddSegment(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		JobID   string         `json:"jobId"`
		Segment domain.Segment `json:"segment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.queue.AddSegment(r.Context(), payload.JobID, payload.Segment); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
