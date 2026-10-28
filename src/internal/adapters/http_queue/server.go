package http_queue

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
)

type HTTPServerQueue struct {
	queue         ports.JobQueue
	internalToken string
}

func NewHTTPServerQueue(queue ports.JobQueue, internalToken string) *HTTPServerQueue {
	return &HTTPServerQueue{queue: queue, internalToken: internalToken}
}

// tokenMiddleware wraps a handler with internal token validation (L3).
func (h *HTTPServerQueue) tokenMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.internalToken != "" {
			token := r.Header.Get("X-Internal-Token")
			if token != h.internalToken {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (h *HTTPServerQueue) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/internal/queue/dequeue", h.tokenMiddleware(h.handleDequeue))
	mux.HandleFunc("/internal/queue/update", h.tokenMiddleware(h.handleUpdate))
	mux.HandleFunc("/internal/queue/add_segment", h.tokenMiddleware(h.handleAddSegment))
	mux.HandleFunc("/internal/queue/get_job", h.tokenMiddleware(h.handleGetJob))
	mux.HandleFunc("/internal/queue/heartbeat", h.tokenMiddleware(h.handleHeartbeat))
	mux.HandleFunc("/internal/queue/get_segment", h.tokenMiddleware(h.handleGetSegment))
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
	workerID := r.URL.Query().Get("worker_id")
	job, err := h.queue.Dequeue(r.Context(), workerID)
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

	if job.Status == "Processing" {
		if job.RestartCount > 0 {
			log.Printf("[HTTP Queue] 🔄 WORKER RECOVERY: Worker %s picked up crashed job %s (Restart #%d)",
				job.WorkerID, job.ID, job.RestartCount)
		} else {
			log.Printf("[HTTP Queue] Worker %s started processing job %s", job.WorkerID, job.ID)
		}
		job.RecoveryInProgress = false
	} else if job.Status == "Ready" {
		log.Printf("[HTTP Queue] Worker %s reports job %s is READY (Stream Active)", job.WorkerID, job.ID)
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

// Cheap heartbeat endpoint
func (h *HTTPServerQueue) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")
	workerID := r.URL.Query().Get("worker_id")
	if jobID == "" || workerID == "" {
		http.Error(w, "id and worker_id required", http.StatusBadRequest)
		return
	}
	if err := h.queue.Heartbeat(r.Context(), jobID, workerID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Single-segment lookup endpoint
func (h *HTTPServerQueue) handleGetSegment(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")
	seqStr := r.URL.Query().Get("seq")
	if jobID == "" || seqStr == "" {
		http.Error(w, "id and seq required", http.StatusBadRequest)
		return
	}
	var seqID int
	if _, err := fmt.Sscanf(seqStr, "%d", &seqID); err != nil {
		http.Error(w, "invalid seq", http.StatusBadRequest)
		return
	}
	seg, err := h.queue.GetSegment(r.Context(), jobID, seqID)
	if err != nil {
		http.Error(w, "Segment not found", http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(seg)
}
