package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Muster-dev/muster-fleet-cloud/pkg/config"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/crypto"
)

func TestStateFileTracksConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "config.json")
	cfg := config.DefaultAgentConfig()
	cfg.Relay.URL = "ws://127.0.0.1:1"
	cfg.Relay.Token = "mst_node_secret"
	cfg.Identity.Name = "prod-1"
	cfg.Identity.OrgID = "org_a"
	keys, _ := crypto.GenerateKeyPair()
	a, err := New(cfg, keys, Options{ConfigPath: path, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadState(path); err == nil {
		t.Fatal("state file exists before connecting")
	}
	a.writeState(true)
	s, err := ReadState(path)
	if err != nil || !s.Live(time.Now()) || s.Name != "prod-1" || s.OrgID != "org_a" || s.PID != os.Getpid() || s.ConnectedSince == nil || s.Relay != "ws://127.0.0.1:1" {
		t.Fatalf("connected state %+v %v", s, err)
	}
	since := *s.ConnectedSince
	a.writeState(true) // heartbeat refresh keeps connected_since
	if s, _ = ReadState(path); !s.ConnectedSince.Equal(since) {
		t.Fatalf("connected_since moved on refresh")
	}
	if s.Live(time.Now().Add(StateFreshness + time.Second)) {
		t.Fatal("stale state counted as live")
	}
	raw, _ := os.ReadFile(config.AgentStatePath(path))
	if strings.Contains(string(raw), "mst_") {
		t.Fatal("state file contains credential material")
	}
	a.writeState(false)
	if s, _ = ReadState(path); s.Live(time.Now()) || s.ConnectedSince != nil {
		t.Fatalf("disconnected state %+v", s)
	}
}
