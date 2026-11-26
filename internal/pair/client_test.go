package pair

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeRelay answers /pair/start once and then serves scripted /pair/token
// replies (status, body) in order; the last one repeats.
type fakeRelay struct {
	t       *testing.T
	mu      sync.Mutex
	start   map[string]any
	replies []reply
	polls   int
	codes   []string
}

type reply struct {
	status int
	body   string
}

func (f *fakeRelay) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/pair/start", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&f.start)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pair_code":"KX7M-42QP","device_code":"dc123","verification_uri":"https://cloud.test/pair","verification_uri_complete":"https://cloud.test/pair?code=KX7M-42QP","expires_in":600,"interval":3}`))
	})
	mux.HandleFunc("POST /api/v1/pair/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.codes = append(f.codes, body["device_code"])
		rp := f.replies[min(f.polls, len(f.replies)-1)]
		f.polls++
		f.mu.Unlock()
		w.WriteHeader(rp.status)
		_, _ = w.Write([]byte(rp.body))
	})
	return mux
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

func setup(t *testing.T, replies ...reply) (*Client, *fakeRelay, *fakeClock, *StartResponse) {
	t.Helper()
	f := &fakeRelay{t: t, replies: replies}
	ts := httptest.NewServer(f.handler())
	t.Cleanup(ts.Close)
	clk := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	c := &Client{BaseURL: ts.URL + "/", UserAgent: "muster-agent/test", Sleep: clk.Sleep, Now: clk.Now}
	start, err := c.Start(context.Background(), StartRequest{Name: "prod-1", Hostname: "box", OS: "linux", Arch: "amd64", ProjectDirSet: true, AgentVersion: "0.4.0"})
	if err != nil {
		t.Fatal(err)
	}
	return c, f, clk, start
}

const pending = `{"error":"authorization_pending"}`

func TestStartSendsMachineInfo(t *testing.T) {
	_, f, _, start := setup(t, reply{400, pending})
	if start.PairCode != "KX7M-42QP" || start.DeviceCode != "dc123" || start.Interval != 3 || start.ExpiresIn != 600 ||
		start.VerificationURIComplete != "https://cloud.test/pair?code=KX7M-42QP" {
		t.Fatalf("start response %+v", start)
	}
	want := map[string]any{"name": "prod-1", "hostname": "box", "os": "linux", "arch": "amd64", "project_dir_set": true, "agent_version": "0.4.0"}
	for k, v := range want {
		if f.start[k] != v {
			t.Errorf("start body %s = %v, want %v", k, f.start[k], v)
		}
	}
}

func TestWaitApprovedHonoursIntervalAndSlowDown(t *testing.T) {
	c, f, clk, start := setup(t,
		reply{400, pending},
		reply{400, `{"error":"slow_down"}`},
		reply{429, `{"error":"rate limited"}`},
		reply{502, `<html>bad gateway</html>`},
		reply{400, pending},
		reply{200, `{"join_token":"mst_agent_abc","org_id":"org_a","name":"prod-web","relay_url":"wss://relay.test"}`},
	)
	var seen []string
	c.OnPoll = func(s string) { seen = append(seen, s) }
	res, err := c.Wait(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if res.JoinToken != "mst_agent_abc" || res.OrgID != "org_a" || res.Name != "prod-web" || res.RelayURL != "wss://relay.test" {
		t.Fatalf("result %+v", res)
	}
	s := time.Second
	wantSleeps := []time.Duration{3 * s, 3 * s, 8 * s, 13 * s, 13 * s, 13 * s}
	if len(clk.sleeps) != len(wantSleeps) {
		t.Fatalf("sleeps %v, want %v", clk.sleeps, wantSleeps)
	}
	for i := range wantSleeps {
		if clk.sleeps[i] != wantSleeps[i] {
			t.Fatalf("sleeps %v, want %v", clk.sleeps, wantSleeps)
		}
	}
	if len(seen) != 5 || seen[1] != "slow_down" || seen[2] != "rate_limited" || seen[3] != "network_error" {
		t.Fatalf("poll statuses %v", seen)
	}
	for _, dc := range f.codes {
		if dc != "dc123" {
			t.Fatalf("polled with device code %q", dc)
		}
	}
}

func TestWaitDeniedAndExpired(t *testing.T) {
	c, _, _, start := setup(t, reply{400, pending}, reply{400, `{"error":"access_denied"}`})
	if _, err := c.Wait(context.Background(), start); !errors.Is(err, ErrDenied) {
		t.Fatalf("denied: %v", err)
	}
	c, _, _, start = setup(t, reply{400, `{"error":"expired_token"}`})
	if _, err := c.Wait(context.Background(), start); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired_token: %v", err)
	}
	// Never approved: gives up when expires_in has passed.
	c, f, clk, start := setup(t, reply{400, pending})
	if _, err := c.Wait(context.Background(), start); !errors.Is(err, ErrExpired) {
		t.Fatalf("deadline: %v", err)
	}
	if f.polls != 200 || clk.Now().Sub(time.Unix(1_700_000_000, 0)) != 600*time.Second {
		t.Fatalf("polls=%d elapsed=%v", f.polls, clk.Now().Sub(time.Unix(1_700_000_000, 0)))
	}
}

func TestWaitCancelled(t *testing.T) {
	c, _, _, start := setup(t, reply{400, pending})
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	c.OnPoll = func(string) {
		if n++; n == 2 {
			cancel()
		}
	}
	if _, err := c.Wait(ctx, start); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestStartAgainstOldRelay(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	defer ts.Close()
	c := &Client{BaseURL: ts.URL}
	if _, err := c.Start(context.Background(), StartRequest{Hostname: "x"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("old relay: %v", err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"name must be 1-32 chars of [A-Za-z0-9._-] (or send a hostname)"}`))
	}))
	defer bad.Close()
	c.BaseURL = bad.URL
	if _, err := c.Start(context.Background(), StartRequest{}); err == nil || err.Error() != "start pairing: name must be 1-32 chars of [A-Za-z0-9._-] (or send a hostname)" {
		t.Fatalf("relay error not surfaced: %v", err)
	}
}
