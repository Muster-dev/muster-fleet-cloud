// Package pair implements the agent side of machine pairing: ask the relay
// for a short code, then poll until a signed-in user approves it on the
// dashboard and the relay hands out a one-time join token.
// See docs/cloud-api.md → Machine pairing.
package pair

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Errors returned by Wait.
var (
	ErrDenied      = errors.New("pairing was denied on the dashboard")
	ErrExpired     = errors.New("pairing code expired")
	ErrUnsupported = errors.New("this relay does not support pairing (needs muster-cloud 0.3.3 or newer)")
)

// slowDownStep is how much the poll interval grows on slow_down / 429
// (RFC 8628 §3.5).
const slowDownStep = 5 * time.Second

// StartRequest describes this machine to the relay (shown on the approval
// page).
type StartRequest struct {
	Name          string `json:"name,omitempty"`
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	ProjectDirSet bool   `json:"project_dir_set"`
	AgentVersion  string `json:"agent_version"`
}

// StartResponse is the relay's answer to /pair/start.
type StartResponse struct {
	PairCode                string `json:"pair_code"`
	DeviceCode              string `json:"device_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// Result is a successful pairing: a one-time join token bound to OrgID and
// Name.
type Result struct {
	JoinToken string `json:"join_token"`
	OrgID     string `json:"org_id"`
	Name      string `json:"name"`
	RelayURL  string `json:"relay_url"`
}

// Client talks to the relay's pairing endpoints.
type Client struct {
	// BaseURL is the relay's HTTP(S) base URL, e.g. https://relay.getmuster.dev.
	BaseURL   string
	UserAgent string
	HTTP      *http.Client
	// Sleep waits for d or until ctx is done (tests replace it).
	Sleep func(ctx context.Context, d time.Duration) error
	// Now is the clock used for the code's expiry (tests replace it).
	Now func() time.Time
	// OnPoll, if set, is called after every poll with the relay's status
	// ("authorization_pending", "slow_down", "rate_limited", "network_error").
	OnPoll func(status string)
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// post sends a JSON body and decodes a JSON reply (success or
// {"error":...}) into out. It returns the HTTP status.
func (c *Client) post(ctx context.Context, path string, body, out any) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return resp.StatusCode, nil // caller decides; body may not be JSON
	}
	if err := json.Unmarshal(data, out); err != nil {
		return resp.StatusCode, fmt.Errorf("unexpected response from relay (HTTP %d)", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

type errBody struct {
	Error string `json:"error"`
}

// Start asks the relay for a pairing code.
func (c *Client) Start(ctx context.Context, req StartRequest) (*StartResponse, error) {
	var out struct {
		StartResponse
		errBody
	}
	status, err := c.post(ctx, "/api/v1/pair/start", req, &out)
	if err != nil {
		return nil, fmt.Errorf("start pairing: %w", err)
	}
	switch {
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
		return nil, ErrUnsupported
	case status == http.StatusTooManyRequests:
		return nil, errors.New("start pairing: rate limited by the relay; wait a minute and try again")
	case status != http.StatusOK:
		if out.Error != "" {
			return nil, fmt.Errorf("start pairing: %s", out.Error)
		}
		return nil, fmt.Errorf("start pairing: relay returned HTTP %d", status)
	case out.DeviceCode == "" || out.PairCode == "":
		return nil, errors.New("start pairing: incomplete response from relay")
	}
	return &out.StartResponse, nil
}

// Wait polls until the code is approved (returning the join token), denied
// (ErrDenied), expires (ErrExpired) or ctx is cancelled. It honours the
// relay's interval and slow_down, backs off on 429, and keeps polling
// through transient network or server errors.
func (c *Client) Wait(ctx context.Context, start *StartResponse) (*Result, error) {
	interval := time.Duration(start.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	expiresIn := time.Duration(start.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = 10 * time.Minute
	}
	deadline := c.now().Add(expiresIn)
	notify := func(s string) {
		if c.OnPoll != nil {
			c.OnPoll(s)
		}
	}
	for c.now().Before(deadline) {
		if err := c.sleep(ctx, interval); err != nil {
			return nil, err
		}
		var out struct {
			Result
			errBody
		}
		status, err := c.post(ctx, "/api/v1/pair/token", map[string]string{"device_code": start.DeviceCode}, &out)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			notify("network_error")
			continue
		}
		if status == http.StatusOK {
			if out.JoinToken == "" {
				return nil, errors.New("relay approved the pairing but sent no join token")
			}
			return &out.Result, nil
		}
		switch out.Error {
		case "authorization_pending":
			notify(out.Error)
		case "slow_down":
			interval += slowDownStep
			notify(out.Error)
		case "access_denied":
			return nil, ErrDenied
		case "expired_token":
			return nil, ErrExpired
		default:
			switch {
			case status == http.StatusTooManyRequests:
				interval += slowDownStep
				notify("rate_limited")
			case status >= 500:
				notify("network_error")
			case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
				return nil, ErrUnsupported
			default:
				return nil, fmt.Errorf("pairing failed (HTTP %d %s)", status, out.Error)
			}
		}
	}
	return nil, ErrExpired
}
