package http_queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/yaffw/yaffw/src/internal/domain"
)

type HTTPClientQueue struct {
	baseURL string
	client  *http.Client
}

func NewHTTPClientQueue(baseURL string) *HTTPClientQueue {
	return &HTTPClientQueue{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

func (q *HTTPClientQueue) Enqueue(ctx context.Context, job *domain.TranscodeJob) error {
	// In the real system, Control Plane enqueues to Redis directly.
	// In this mock, we don't need this method in the Compute Plane client
	// because Compute only Dequeues.
	return errors.New("not implemented for client")
}

func (q *HTTPClientQueue) Dequeue(ctx context.Context) (*domain.TranscodeJob, error) {
	resp, err := q.client.Get(q.baseURL + "/dequeue")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, errors.New("empty queue")
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var job domain.TranscodeJob
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, err
	}
	return &job, nil
}

func (q *HTTPClientQueue) GetJob(ctx context.Context, jobID string) (*domain.TranscodeJob, error) {
	// Used by Control Plane, not Compute client usually
	return nil, errors.New("not implemented")
}

func (q *HTTPClientQueue) UpdateJob(ctx context.Context, job *domain.TranscodeJob) error {
	// Ensure heartbeat is set locally if not zero (caller usually sets it)
	if job.LastHeartbeat.IsZero() {
		job.LastHeartbeat = time.Now()
	}

	body, _ := json.Marshal(job)
	req, _ := http.NewRequestWithContext(ctx, "POST", q.baseURL+"/update", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to update: %d", resp.StatusCode)
	}
	return nil
}

func (q *HTTPClientQueue) AddSegment(ctx context.Context, jobID string, segment domain.Segment) error {
	payload := struct {
		JobID   string         `json:"jobId"`
		Segment domain.Segment `json:"segment"`
	}{
		JobID:   jobID,
		Segment: segment,
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST", q.baseURL+"/add_segment", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to add segment: %d", resp.StatusCode)
	}
	return nil
}

func (q *HTTPClientQueue) MarkWorkerAsDead(ctx context.Context, jobID string, workerID string) (int, error) {
	return 0, errors.New("not implemented for client")
}

func (q *HTTPClientQueue) PurgeJobs(ctx context.Context, jobID string) error {
	return errors.New("not implemented for client")
}

func (q *HTTPClientQueue) ListActiveJobs(ctx context.Context) ([]*domain.TranscodeJob, error) {
	// Not used by worker
	return nil, errors.New("not implemented")
}
