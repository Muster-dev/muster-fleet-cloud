package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAgentConfigAtomic0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	cfg := DefaultAgentConfig()
	cfg.Relay.Token = "mst_node_secret"
	if err := SaveAgentConfigTo(path, cfg); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Errorf("config perms %o, want 600", st.Mode().Perm())
	}
	dst, _ := os.Stat(filepath.Dir(path))
	if dst.Mode().Perm() != 0700 {
		t.Errorf("dir perms %o, want 700", dst.Mode().Perm())
	}
	got, err := LoadAgentConfigFrom(path)
	if err != nil || got.Relay.Token != "mst_node_secret" {
		t.Fatalf("reload: %+v %v", got, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestCLIConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloud.json")
	c, err := LoadCLIConfig(path)
	if err != nil || c.Token != "" {
		t.Fatalf("missing file: %+v %v", c, err)
	}
	if err := SaveCLIConfig(path, &CLIConfig{RelayURL: "wss://r", Token: "mst_cli_x"}); err != nil {
		t.Fatal(err)
	}
	c, _ = LoadCLIConfig(path)
	if c.Token != "mst_cli_x" {
		t.Fatalf("got %+v", c)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0600 {
		t.Errorf("perms %o", st.Mode().Perm())
	}
}
