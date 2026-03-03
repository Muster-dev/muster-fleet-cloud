package tunnel

import (
	"testing"
	"time"
)

func TestBackoffReturnsBaseDelayOnAttemptZero(t *testing.T) {
	cfg := ReconnectConfig{
		BaseDelay: 1 * time.Second,
		MaxDelay:  60 * time.Second,
		MaxJitter: 0, // no jitter for deterministic test
	}

	delay := backoff(0, cfg)
	if delay != cfg.BaseDelay {
		t.Errorf("backoff(0): got %v, want %v", delay, cfg.BaseDelay)
	}
}

func TestBackoffCapsAtMaxDelay(t *testing.T) {
	cfg := ReconnectConfig{
		BaseDelay: 1 * time.Second,
		MaxDelay:  60 * time.Second,
		MaxJitter: 0,
	}

	// attempt 10 => 2^10 * 1s = 1024s, should cap at 60s
	delay := backoff(10, cfg)
	if delay > cfg.MaxDelay {
		t.Errorf("backoff(10): got %v, exceeds max %v", delay, cfg.MaxDelay)
	}
	if delay != cfg.MaxDelay {
		t.Errorf("backoff(10): got %v, want max %v", delay, cfg.MaxDelay)
	}
}

func TestBackoffAddsJitter(t *testing.T) {
	cfg := ReconnectConfig{
		BaseDelay: 1 * time.Second,
		MaxDelay:  60 * time.Second,
		MaxJitter: 500 * time.Millisecond,
	}

	// Run multiple times and check that not all values are identical
	results := make(map[time.Duration]bool)
	for i := 0; i < 50; i++ {
		d := backoff(0, cfg)
		results[d] = true

		// Each result should be between BaseDelay and BaseDelay + MaxJitter
		if d < cfg.BaseDelay {
			t.Errorf("backoff(0) = %v, less than BaseDelay %v", d, cfg.BaseDelay)
		}
		if d > cfg.BaseDelay+cfg.MaxJitter {
			t.Errorf("backoff(0) = %v, exceeds BaseDelay+MaxJitter %v", d, cfg.BaseDelay+cfg.MaxJitter)
		}
	}

	if len(results) < 2 {
		t.Error("backoff produced identical results across 50 runs; jitter appears broken")
	}
}
