package sources

import (
	"testing"
	"time"
)

func TestStartupJitter_Bounded(t *testing.T) {
	// With a large interval the jitter is capped at maxStartupJitter.
	for i := 0; i < 1000; i++ {
		d := startupJitter(time.Hour)
		if d < 0 || d >= maxStartupJitter {
			t.Fatalf("jitter %v out of [0, %v)", d, maxStartupJitter)
		}
	}
}

func TestStartupJitter_CappedByInterval(t *testing.T) {
	// A sub-jitter interval bounds the jitter to the interval, never overshoots.
	interval := 100 * time.Millisecond
	for i := 0; i < 1000; i++ {
		d := startupJitter(interval)
		if d < 0 || d >= interval {
			t.Fatalf("jitter %v out of [0, %v)", d, interval)
		}
	}
}

func TestStartupJitter_NonPositive(t *testing.T) {
	if d := startupJitter(0); d != 0 {
		t.Fatalf("zero interval should yield zero jitter, got %v", d)
	}
	if d := startupJitter(-time.Second); d != 0 {
		t.Fatalf("negative interval should yield zero jitter, got %v", d)
	}
}
