package services

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetrics_SetGauge(t *testing.T) {
	m := NewMetrics()
	m.SetGauge("test_gauge", "test help", 42)

	output := captureMetricsOutput(m)
	if !strings.Contains(output, "test_gauge 42") {
		t.Errorf("expected output to contain 'test_gauge 42', got:\n%s", output)
	}
	if !strings.Contains(output, "# TYPE test_gauge gauge") {
		t.Errorf("expected output to contain gauge type declaration")
	}
}

func TestMetrics_IncCounter(t *testing.T) {
	m := NewMetrics()
	m.IncCounter("test_counter", "test counter help")
	m.IncCounter("test_counter", "test counter help")

	output := captureMetricsOutput(m)
	if !strings.Contains(output, "test_counter 2") {
		t.Errorf("expected output to contain 'test_counter 2', got:\n%s", output)
	}
	if !strings.Contains(output, "# TYPE test_counter counter") {
		t.Errorf("expected output to contain counter type declaration")
	}
}

func TestMetrics_GoRoutines(t *testing.T) {
	m := NewMetrics()
	output := captureMetricsOutput(m)
	if !strings.Contains(output, "go_goroutines") {
		t.Errorf("expected output to contain go_goroutines")
	}
}

func TestGlobalMetrics_Helpers(t *testing.T) {
	SetQueueDepth(5)
	SetActiveJobs(3)
	IncRecoveryCount()
	IncSegmentReported()

	output := captureMetricsOutput(GlobalMetrics)
	if !strings.Contains(output, "yaffw_queue_depth 5") {
		t.Errorf("expected yaffw_queue_depth 5 in output")
	}
	if !strings.Contains(output, "yaffw_jobs_active 3") {
		t.Errorf("expected yaffw_jobs_active 3 in output")
	}
	if !strings.Contains(output, "yaffw_recovery_total 1") {
		t.Errorf("expected yaffw_recovery_total 1 in output")
	}
	if !strings.Contains(output, "yaffw_segments_total 1") {
		t.Errorf("expected yaffw_segments_total 1 in output")
	}
}

func captureMetricsOutput(m *Metrics) string {
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, nil)
	return rec.Body.String()
}
