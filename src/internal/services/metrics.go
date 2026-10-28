package services

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// Metrics is a lightweight Prometheus-compatible metrics collector (L2).
// No external dependencies — outputs plain text in Prometheus exposition format.
type Metrics struct {
	mu       sync.RWMutex
	gauges   map[string]*gaugeEntry
	counters map[string]*counterEntry
}

type gaugeEntry struct {
	help   string
	value  int64
	labels map[string]string
}

type counterEntry struct {
	help   string
	value  int64
	labels map[string]string
}

var GlobalMetrics = NewMetrics()

func NewMetrics() *Metrics {
	return &Metrics{
		gauges:   make(map[string]*gaugeEntry),
		counters: make(map[string]*counterEntry),
	}
}

func (m *Metrics) SetGauge(name, help string, value int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.gauges[name]; ok {
		atomic.StoreInt64(&e.value, value)
		return
	}
	m.gauges[name] = &gaugeEntry{help: help, value: value}
}

func (m *Metrics) IncCounter(name, help string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.counters[name]; ok {
		atomic.AddInt64(&e.value, 1)
		return
	}
	m.counters[name] = &counterEntry{help: help, value: 1}
}

func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	m.mu.RLock()
	defer m.mu.RUnlock()

	var sb strings.Builder

	// Gauges
	for name, e := range m.gauges {
		sb.WriteString(fmt.Sprintf("# HELP %s %s\n", name, e.help))
		sb.WriteString(fmt.Sprintf("# TYPE %s gauge\n", name))
		sb.WriteString(fmt.Sprintf("%s %d\n", name, atomic.LoadInt64(&e.value)))
	}

	// Counters
	for name, e := range m.counters {
		sb.WriteString(fmt.Sprintf("# HELP %s %s\n", name, e.help))
		sb.WriteString(fmt.Sprintf("# TYPE %s counter\n", name))
		sb.WriteString(fmt.Sprintf("%s %d\n", name, atomic.LoadInt64(&e.value)))
	}

	// Go runtime basics
	sb.WriteString(fmt.Sprintf("# HELP go_goroutines Number of goroutines\n"))
	sb.WriteString(fmt.Sprintf("# TYPE go_goroutines gauge\n"))
	sb.WriteString(fmt.Sprintf("go_goroutines %d\n", runtime.NumGoroutine()))

	w.Write([]byte(sb.String()))
}

// IncRecoveryCount is a convenience helper.
func IncRecoveryCount() {
	GlobalMetrics.IncCounter("yaffw_recovery_total", "Total number of recovery events triggered")
}

// IncSegmentReported is a convenience helper.
func IncSegmentReported() {
	GlobalMetrics.IncCounter("yaffw_segments_total", "Total segments reported by workers")
}

// SetQueueDepth updates the current queue depth gauge.
func SetQueueDepth(depth int) {
	GlobalMetrics.SetGauge("yaffw_queue_depth", "Number of pending transcode jobs", int64(depth))
}

// SetActiveJobs updates the active jobs gauge.
func SetActiveJobs(count int) {
	GlobalMetrics.SetGauge("yaffw_jobs_active", "Number of active (non-idle) transcode jobs", int64(count))
}
