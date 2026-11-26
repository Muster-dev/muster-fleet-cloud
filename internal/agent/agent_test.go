package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Muster-dev/muster-fleet-cloud/pkg/config"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/crypto"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/protocol"
)

// On enrollment the relay returns a node token; the agent must persist it
// (0600) in place of the one-time join token and use it from then on.
func TestOnAuthPersistsNodeToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "config.json")
	cfg := config.DefaultAgentConfig()
	cfg.Relay.URL = "ws://127.0.0.1:1"
	cfg.Relay.Token = "mst_agent_join"
	cfg.Identity.Name = "requested"
	keys, _ := crypto.GenerateKeyPair()
	a, err := New(cfg, keys, Options{ConfigPath: path, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.onAuth(&protocol.AuthResponse{OK: true, OrgID: "org_a", Name: "prod-1", NodeToken: "mst_node_secret"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("config not written 0600: %v %v", st, err)
	}
	got, err := config.LoadAgentConfigFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Relay.Token != "mst_node_secret" || got.Identity.OrgID != "org_a" || got.Identity.Name != "prod-1" {
		t.Fatalf("persisted %+v", got)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "mst_agent_join") {
		t.Fatal("join token still in config")
	}

	// Without a config path, a node token cannot be persisted → error rather
	// than silently losing the credential.
	cfg2 := config.DefaultAgentConfig()
	cfg2.Relay.URL = "ws://127.0.0.1:1"
	b, _ := New(cfg2, keys, Options{Logf: func(string, ...any) {}})
	if err := b.onAuth(&protocol.AuthResponse{OK: true, NodeToken: "mst_node_x"}); err == nil {
		t.Fatal("expected error when node token cannot be persisted")
	}
}
