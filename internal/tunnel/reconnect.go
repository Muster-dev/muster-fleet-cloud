package tunnel

import (
	"context"
	"log"
	"math"
	"math/rand"
	"time"
)

// ReconnectConfig controls reconnection behavior.
type ReconnectConfig struct {
	BaseDelay time.Duration
	MaxDelay  time.Duration
	MaxJitter time.Duration
}

// DefaultReconnectConfig returns sensible reconnection defaults.
func DefaultReconnectConfig() ReconnectConfig {
	return ReconnectConfig{
		BaseDelay: 1 * time.Second,
		MaxDelay:  60 * time.Second,
		MaxJitter: 500 * time.Millisecond,
	}
}

// ConnectLoop connects to the relay and reconnects on failure.
// It calls onConnected each time a connection is established.
// The loop runs until ctx is cancelled.
func (c *Client) ConnectLoop(ctx context.Context, cfg ReconnectConfig, onConnected func(ctx context.Context) error) error {
	attempt := 0

	for {
		err := c.Connect()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			delay := backoff(attempt, cfg)
			log.Printf("connection failed: %v, reconnecting in %s", err, delay)
			select {
			case <-time.After(delay):
				attempt++
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		// Connection established
		attempt = 0
		connectedAt := time.Now()

		if err := onConnected(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			// If connection lasted > 60s, reset attempt counter
			if time.Since(connectedAt) > 60*time.Second {
				attempt = 0
			}

			delay := backoff(attempt, cfg)
			log.Printf("disconnected: %v, reconnecting in %s", err, delay)
			select {
			case <-time.After(delay):
				attempt++
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func backoff(attempt int, cfg ReconnectConfig) time.Duration {
	delay := float64(cfg.BaseDelay) * math.Pow(2, float64(attempt))
	if delay > float64(cfg.MaxDelay) {
		delay = float64(cfg.MaxDelay)
	}

	jitter := time.Duration(rand.Int63n(int64(cfg.MaxJitter)))
	return time.Duration(delay) + jitter
}
