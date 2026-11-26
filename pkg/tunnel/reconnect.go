package tunnel

import (
	"context"
	"math"
	"math/rand"
	"time"

	"github.com/Muster-dev/muster-fleet-cloud/pkg/protocol"
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

// ConnectLoop connects to the relay and reconnects on failure until ctx is
// cancelled.
//
// After each successful authentication, onAuth (optional) is called with the
// relay's response — agents use it to persist a node token issued on
// enrollment and switch to it via SetToken before any reconnect. Then
// onConnected runs the session; when it returns the loop reconnects.
//
// A rejected credential (*AuthError) is permanent and is returned
// immediately instead of retrying forever with a token the relay will never
// accept (e.g. a revoked node token).
func (c *Client) ConnectLoop(
	ctx context.Context,
	cfg ReconnectConfig,
	onAuth func(*protocol.AuthResponse) error,
	onConnected func(ctx context.Context) error,
) error {
	attempt := 0

	wait := func(reason string, err error) error {
		delay := backoff(attempt, cfg)
		c.opts.Logf("%s: %v, reconnecting in %s", reason, err, delay.Round(time.Millisecond))
		attempt++
		select {
		case <-time.After(delay):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		dctx, cancel := context.WithTimeout(ctx, DefaultDialTimeout)
		err := c.ConnectContext(dctx)
		cancel()
		if err != nil {
			if werr := wait("connection failed", err); werr != nil {
				return werr
			}
			continue
		}

		resp, err := c.Authenticate()
		if err != nil {
			c.Close()
			if IsAuthError(err) {
				return err
			}
			if werr := wait("auth failed", err); werr != nil {
				return werr
			}
			continue
		}
		if onAuth != nil {
			if err := onAuth(resp); err != nil {
				c.Close()
				return err
			}
		}

		connectedAt := time.Now()
		err = onConnected(ctx)
		c.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A connection that stayed up for a while resets the backoff.
		if time.Since(connectedAt) > 60*time.Second {
			attempt = 0
		}
		if werr := wait("disconnected", err); werr != nil {
			return werr
		}
	}
}

func backoff(attempt int, cfg ReconnectConfig) time.Duration {
	delay := float64(cfg.BaseDelay) * math.Pow(2, float64(attempt))
	if delay > float64(cfg.MaxDelay) {
		delay = float64(cfg.MaxDelay)
	}

	var jitter time.Duration
	if cfg.MaxJitter > 0 {
		jitter = time.Duration(rand.Int63n(int64(cfg.MaxJitter)))
	}
	return time.Duration(delay) + jitter
}
