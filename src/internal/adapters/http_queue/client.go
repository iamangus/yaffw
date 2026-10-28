package http_queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
)

type HTTPClientQueue struct {
	baseURL       string
	client        *http.Client
	internalToken string
}

func NewHTTPClientQueue(baseURL string, internalToken string) *HTTPClientQueue {
	return &HTTPClientQueue{
		baseURL:       baseURL,
		client:        &http.Client{Timeout: 5 * time.Second},
		internalToken: internalToken,
	}
}

func (q *HTTPClientQueue) newRequest(ctx context.Context, method, url string, body []byte) (*http.Request, error) {
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequestWithContext(ctx, method, url, bytes.NewBuffer(body))
	} else {
		req, err = http.NewRequestWithContext(ctx, method, url, nil)
	}
	if err != nil {
		return nil, err
	}
	if q.internalToken != "" {
		req.Header.Set("X-Internal-Token", q.internalToken)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (q *HTTPClientQueue) Enqueue(ctx context.Context, job *domain.TranscodeJob) error {
	return errors.New("not implemented for client")
}

func (q *HTTPClientQueue) Dequeue(ctx context.Context, workerID string) (*domain.TranscodeJob, error) {
	req, err := q.newRequest(ctx, "GET", q.baseURL+"/dequeue?worker_id="+workerID, nil)
	if err != nil {
		return nil, err
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, ports.ErrEmptyQueue
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
	req, err := q.newRequest(ctx, "GET", q.baseURL+"/get_job?id="+jobID, nil)
	if err != nil {
		return nil, err
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("job not found: %d", resp.StatusCode)
	}

	var job domain.TranscodeJob
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, err
	}
	return &job, nil
}

func (q *HTTPClientQueue) UpdateJob(ctx context.Context, job *domain.TranscodeJob) error {
	if job.LastHeartbeat.IsZero() {
		job.LastHeartbeat = time.Now()
	}

	body, _ := json.Marshal(job)
	req, err := q.newRequest(ctx, "POST", q.baseURL+"/update", body)
	if err != nil {
		return err
	}
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
	req, err := q.newRequest(ctx, "POST", q.baseURL+"/add_segment", body)
	if err != nil {
		return err
	}
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

func (q *HTTPClientQueue) MarkWorkerAsDead(ctx context.Context, jobID string, workerID string) error {
	return errors.New("not implemented for client")
}

func (q *HTTPClientQueue) PurgeJobs(ctx context.Context, jobID string) error {
	return errors.New("not implemented for client")
}

func (q *HTTPClientQueue) ListActiveJobs(ctx context.Context) ([]*domain.TranscodeJob, error) {
	return nil, errors.New("not implemented")
}

func (q *HTTPClientQueue) DeleteJob(ctx context.Context, jobID string) error {
	return errors.New("not implemented for client")
}

func (q *HTTPClientQueue) TouchJob(ctx context.Context, jobID string) error {
	return errors.New("not implemented for client")
}

// Cheap heartbeat — lightweight HTTP call, no body, no segment fetch.
func (q *HTTPClientQueue) Heartbeat(ctx context.Context, jobID string, workerID string) error {
	req, err := q.newRequest(ctx, "GET", q.baseURL+"/heartbeat?id="+jobID+"&worker_id="+workerID, nil)
	if err != nil {
		return err
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("heartbeat failed: %d", resp.StatusCode)
	}
	return nil
}

// Single-segment lookup.
func (q *HTTPClientQueue) GetSegment(ctx context.Context, jobID string, seqID int) (*domain.Segment, error) {
	req, err := q.newRequest(ctx, "GET", q.baseURL+"/get_segment?id="+jobID+"&seq="+strconv.Itoa(seqID), nil)
	if err != nil {
		return nil, err
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("segment not found: %d", resp.StatusCode)
	}
	var seg domain.Segment
	if err := json.NewDecoder(resp.Body).Decode(&seg); err != nil {
		return nil, err
	}
	return &seg, nil
}
