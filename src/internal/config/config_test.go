package config

import "testing"

func TestTranscodeConfig_WithDefaults(t *testing.T) {
	cfg := TranscodeConfig{}
	d := cfg.WithDefaults()

	if d.HLSSegmentTime != 4 {
		t.Errorf("expected HLSSegmentTime=4, got %d", d.HLSSegmentTime)
	}
	if d.HeartbeatInterval != 1 {
		t.Errorf("expected HeartbeatInterval=1, got %d", d.HeartbeatInterval)
	}
	if d.StatusCheckInterval != 5 {
		t.Errorf("expected StatusCheckInterval=5, got %d", d.StatusCheckInterval)
	}
	if d.DeadWorkerThreshold != 3 {
		t.Errorf("expected DeadWorkerThreshold=3, got %d", d.DeadWorkerThreshold)
	}
	if d.IdleTimeout != 60 {
		t.Errorf("expected IdleTimeout=60, got %d", d.IdleTimeout)
	}
	if d.CompletedRetention != 300 {
		t.Errorf("expected CompletedRetention=300, got %d", d.CompletedRetention)
	}
	if d.ManifestTimeout != 30 {
		t.Errorf("expected ManifestTimeout=30, got %d", d.ManifestTimeout)
	}
	if d.MaxRestarts != 3 {
		t.Errorf("expected MaxRestarts=3, got %d", d.MaxRestarts)
	}
}

func TestTranscodeConfig_WithDefaults_PreservesExplicitValues(t *testing.T) {
	cfg := TranscodeConfig{
		HLSSegmentTime:      6,
		HeartbeatInterval:   2,
		DeadWorkerThreshold: 10,
		MaxRestarts:         5,
	}
	d := cfg.WithDefaults()

	if d.HLSSegmentTime != 6 {
		t.Errorf("expected HLSSegmentTime=6, got %d", d.HLSSegmentTime)
	}
	if d.HeartbeatInterval != 2 {
		t.Errorf("expected HeartbeatInterval=2, got %d", d.HeartbeatInterval)
	}
	if d.DeadWorkerThreshold != 10 {
		t.Errorf("expected DeadWorkerThreshold=10, got %d", d.DeadWorkerThreshold)
	}
	if d.MaxRestarts != 5 {
		t.Errorf("expected MaxRestarts=5, got %d", d.MaxRestarts)
	}
	// Unset values should still default
	if d.IdleTimeout != 60 {
		t.Errorf("expected IdleTimeout=60, got %d", d.IdleTimeout)
	}
}

func TestDefaultEncoder(t *testing.T) {
	enc := DefaultEncoder()
	if enc.Preset != "veryfast" {
		t.Errorf("expected preset=veryfast, got %s", enc.Preset)
	}
	if enc.CRF != 23 {
		t.Errorf("expected crf=23, got %d", enc.CRF)
	}
	if enc.MaxRate != "5M" {
		t.Errorf("expected maxrate=5M, got %s", enc.MaxRate)
	}
	if enc.BufSize != "10M" {
		t.Errorf("expected bufsize=10M, got %s", enc.BufSize)
	}
}
