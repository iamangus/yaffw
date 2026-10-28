package services

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"
)

// TestParseHLSManifest verifies that the M3U8 parser correctly extracts
// segment numbers, durations, and accumulated start times.
func TestParseHLSManifest(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "stream.m3u8")

	content := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:4
#EXTINF:4.000,
segment_000.ts
#EXTINF:4.000,
segment_001.ts
#EXTINF:3.500,
segment_002.ts
#EXTINF:4.000,
segment_003.ts
#EXT-X-ENDLIST
`
	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	w := &TranscodeWorker{}
	entries := w.parseHLSManifest(manifestPath)

	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}

	expected := []struct {
		seq       int
		duration  float64
		startTime float64
	}{
		{0, 4.0, 0.0},
		{1, 4.0, 4.0},
		{2, 3.5, 8.0},
		{3, 4.0, 11.5},
	}

	for i, exp := range expected {
		if entries[i].seq != exp.seq {
			t.Errorf("entry %d: expected seq=%d, got %d", i, exp.seq, entries[i].seq)
		}
		if entries[i].duration != exp.duration {
			t.Errorf("entry %d: expected duration=%.1f, got %.1f", i, exp.duration, entries[i].duration)
		}
		if entries[i].startTime != exp.startTime {
			t.Errorf("entry %d: expected startTime=%.1f, got %.1f", i, exp.startTime, entries[i].startTime)
		}
	}
}

// TestParseHLSManifest_PartialFile verifies parsing when the manifest is
// still being written (no ENDLIST, ffmpeg still running).
func TestParseHLSManifest_PartialFile(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "stream.m3u8")

	content := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:4
#EXTINF:4.000,
segment_000.ts
#EXTINF:4.000,
segment_001.ts
`
	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	w := &TranscodeWorker{}
	entries := w.parseHLSManifest(manifestPath)

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries from partial manifest, got %d", len(entries))
	}
}

// TestParseHLSManifest_NonExistentFile verifies graceful handling of missing file.
func TestParseHLSManifest_NonExistentFile(t *testing.T) {
	w := &TranscodeWorker{}
	entries := w.parseHLSManifest("/nonexistent/path/stream.m3u8")
	if entries != nil {
		t.Fatalf("expected nil for non-existent file, got %v", entries)
	}
}

// TestParseHLSManifest_EmptyFile verifies graceful handling of empty file.
func TestParseHLSManifest_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "stream.m3u8")
	os.WriteFile(manifestPath, []byte(""), 0644)

	w := &TranscodeWorker{}
	entries := w.parseHLSManifest(manifestPath)
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries from empty file, got %d", len(entries))
	}
}

// Ensure bufio import is used (it's used in worker.go but this test file is in the same package)
var _ = bufio.NewScanner
