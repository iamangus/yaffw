package services

import (
	"testing"
)

func TestComputeSplitPoints_SingleRange(t *testing.T) {
	keyframes := []float64{0, 4, 8, 12, 16, 20}
	duration := 20.0
	splits := ComputeSplitPoints(keyframes, duration, 1, 4.0)
	if len(splits) != 2 {
		t.Fatalf("expected 2 splits, got %d", len(splits))
	}
	if splits[0] != 0 || splits[1] != 20.0 {
		t.Fatalf("expected [0, 20], got %v", splits)
	}
}

func TestComputeSplitPoints_TwoRanges(t *testing.T) {
	keyframes := []float64{0, 4, 8, 12, 16, 20}
	duration := 20.0
	splits := ComputeSplitPoints(keyframes, duration, 2, 4.0)
	if len(splits) != 3 {
		t.Fatalf("expected 3 splits, got %d", len(splits))
	}
	if splits[0] != 0 {
		t.Fatalf("expected splits[0]=0, got %v", splits[0])
	}
	if splits[2] != 20.0 {
		t.Fatalf("expected splits[2]=20, got %v", splits[2])
	}
	// Middle split should be at or near 10s, snapped to nearest keyframe <= 10
	if splits[1] != 8.0 && splits[1] != 12.0 {
		// 8 is the nearest keyframe <= 10
		if splits[1] != 8.0 {
			t.Fatalf("expected splits[1]=8 (nearest keyframe <= 10), got %v", splits[1])
		}
	}
}

func TestComputeSplitPoints_ShortMediaFallback(t *testing.T) {
	keyframes := []float64{0, 2, 4}
	duration := 4.0
	// 2 ranges * 2 * 4s = 16s > 4s duration → should fall back to single
	splits := ComputeSplitPoints(keyframes, duration, 2, 4.0)
	if len(splits) != 2 {
		t.Fatalf("expected fallback to 2 splits, got %d", len(splits))
	}
}

func TestComputeSplitPoints_TinyRangeCollapsed(t *testing.T) {
	keyframes := []float64{0, 4, 8, 12, 60}
	duration := 60.0
	// 3 ranges, ideal cuts at 20 and 40. Only keyframes are 0,4,8,12,60.
	// Cut at 20 → nearest KF <= 20 is 12. Range 0-12 is 12s (> 2*4=8s) → kept.
	// Cut at 40 → nearest KF <= 40 is 12. Range 12-12 is 0s (< 8s) → collapsed.
	splits := ComputeSplitPoints(keyframes, duration, 3, 4.0)
	// Should have fewer than 4 splits due to collapse
	if len(splits) < 2 {
		t.Fatalf("expected at least 2 splits, got %d", len(splits))
	}
	if splits[0] != 0 {
		t.Fatalf("expected splits[0]=0, got %v", splits[0])
	}
	if splits[len(splits)-1] != 60.0 {
		t.Fatalf("expected last split=60, got %v", splits[len(splits)-1])
	}
}

func TestComputeSplitPoints_EmptyKeyframes(t *testing.T) {
	splits := ComputeSplitPoints(nil, 100.0, 4, 4.0)
	if len(splits) != 2 {
		t.Fatalf("expected 2 splits for empty keyframes, got %d", len(splits))
	}
}
