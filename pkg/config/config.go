package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// AgentConfig is the muster-agent daemon configuration.
type AgentConfig struct {
	Relay    RelayConfig    `json:"relay"`
	Identity IdentityConfig `json:"identity"`
	Project  ProjectConfig  `json:"project"`

	MusterPath      string   `json:"muster_path,omitempty"`
	AllowedCommands []string `json:"allowed_commands,omitempty"`
	// AllowShell lets "exec" commands run through `bash -c` with no allowlist
	// check. Anyone holding a CLI token for the org can then run anything on
	// this machine. Off by default.
	AllowShell bool `json:"allow_shell,omitempty"`
	// AllowPlaintext accepts unencrypted COMMAND frames (legacy clients).
	// Off by default: commands must be end-to-end encrypted.
	AllowPlaintext bool   `json:"allow_plaintext,omitempty"`
	LogFile        string `json:"log_file,omitempty"`
	// CLIKeyPolicy controls pinning of CLI identity keys per CLI token:
	// "tofu" (default): the first persistent key seen for a token is pinned
	// and later commands under that token must use it; "strict": only keys
	// already pinned (muster-agent trust add) are accepted; "off": no
	// checks. See docs/protocol.md.
	CLIKeyPolicy string `json:"cli_key_policy,omitempty"`

	ReconnectBaseDelay string `json:"reconnect_base_delay,omitempty"`
	ReconnectMaxDelay  string `json:"reconnect_max_delay,omitempty"`
}

// RelayConfig holds the relay address and this agent's credential.
type RelayConfig struct {
	URL string `json:"url"`
	// Token is the agent's long-lived node credential (mst_node_...), issued
	// by the relay on enrollment. A join token (mst_agent_...) here is
	// exchanged for a node token on first connect and then replaced.
	Token string `json:"token"`
}

// IdentityConfig is the agent's identity as assigned by the relay.
type IdentityConfig struct {
	OrgID string `json:"org_id"`
	Name  string `json:"name"`
}

// ProjectConfig describes the project this agent deploys.
type ProjectConfig struct {
	Dir  string `json:"dir"`
	Mode string `json:"mode"` // "muster" or "push"
}

// CLI key policies (AgentConfig.CLIKeyPolicy).
const (
	CLIKeyPolicyTOFU   = "tofu"
	CLIKeyPolicyStrict = "strict"
	CLIKeyPolicyOff    = "off"
)

// ValidCLIKeyPolicy reports whether p is a known policy ("" means tofu).
func ValidCLIKeyPolicy(p string) bool {
	switch p {
	case "", CLIKeyPolicyTOFU, CLIKeyPolicyStrict, CLIKeyPolicyOff:
		return true
	}
	return false
}

// DefaultAllowedCommands is the exec allowlist written by `muster-agent
// join`. Entries match word by word as a prefix of the command's argv
// ("muster deploy" allows "muster deploy api --force"). Only subcommands
// that exist in the muster CLI are listed; "muster hooks verify" is the
// read-only part of "muster hooks" (approve/lock/unlock are not allowed).
func DefaultAllowedCommands() []string {
	return []string{
		"muster deploy",
		"muster status",
		"muster rollback",
		"muster logs",
		"muster history",
		"muster --version",
		"muster hooks verify",
	}
}

// DefaultAgentConfig returns sensible defaults for the agent.
func DefaultAgentConfig() *AgentConfig {
	return &AgentConfig{
		MusterPath:         "muster",
		AllowedCommands:    DefaultAllowedCommands(),
		ReconnectBaseDelay: "1s",
		ReconnectMaxDelay:  "60s",
	}
}

// AgentConfigDir returns the default agent config directory
// ($MUSTER_AGENT_HOME or ~/.muster-agent).
func AgentConfigDir() string {
	if d := os.Getenv("MUSTER_AGENT_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".muster-agent")
}

// AgentConfigPath returns the default agent config file path
// ($MUSTER_AGENT_CONFIG, or <AgentConfigDir>/config.json).
func AgentConfigPath() string {
	if p := os.Getenv("MUSTER_AGENT_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(AgentConfigDir(), "config.json")
}

// KeyDirFor returns the key directory that belongs to a config file
// (a "keys" directory next to it).
func KeyDirFor(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "keys")
}

// TrustedCLIsPath returns the file holding pinned CLI keys that belongs to
// a config file (trusted_clis.json next to it).
func TrustedCLIsPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "trusted_clis.json")
}

// LoadAgentConfig reads the agent config from the default path.
func LoadAgentConfig() (*AgentConfig, error) {
	return LoadAgentConfigFrom(AgentConfigPath())
}

// LoadAgentConfigFrom reads the agent config from path.
func LoadAgentConfigFrom(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := DefaultAgentConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

// SaveAgentConfig writes the agent config to the default path.
func SaveAgentConfig(cfg *AgentConfig) error {
	return SaveAgentConfigTo(AgentConfigPath(), cfg)
}

// SaveAgentConfigTo atomically writes the agent config to path with 0600
// permissions (it contains the node credential).
func SaveAgentConfigTo(path string, cfg *AgentConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return WriteFileAtomic(path, append(data, '\n'), 0600)
}

// WriteFileAtomic writes data to a temp file in the same directory, fsyncs
// it, and renames it over path, so readers never see a partial file and a
// crash never leaves a truncated credential file. The parent directory is
// created with 0700 if missing.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename into place: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
