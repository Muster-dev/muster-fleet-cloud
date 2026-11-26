package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultRelayURL is the hosted Fleet Cloud relay.
const DefaultRelayURL = "wss://relay.getmuster.dev"

// CLIConfig is muster-tunnel's credential file (~/.muster/cloud.json, 0600).
type CLIConfig struct {
	RelayURL string `json:"relay_url"`
	Token    string `json:"token"`
	TokenID  string `json:"token_id,omitempty"`
	OrgID    string `json:"org_id,omitempty"`
	Name     string `json:"name,omitempty"`
}

// MusterHome returns ~/.muster (or $MUSTER_HOME).
func MusterHome() string {
	if d := os.Getenv("MUSTER_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".muster")
}

// CLIConfigPath returns the CLI credential file path
// ($MUSTER_CLOUD_CONFIG, or ~/.muster/cloud.json).
func CLIConfigPath() string {
	if p := os.Getenv("MUSTER_CLOUD_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(MusterHome(), "cloud.json")
}

// LoadCLIConfig reads the CLI config. A missing file returns an empty config
// and no error.
func LoadCLIConfig(path string) (*CLIConfig, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &CLIConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg CLIConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// SaveCLIConfig atomically writes the CLI config with 0600 permissions.
func SaveCLIConfig(path string, cfg *CLIConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(data, '\n'), 0600)
}

// CLIKeyDir returns the directory holding the CLI's long-lived X25519
// identity key ($MUSTER_CLOUD_KEY_DIR, or ~/.muster/cloud_keys). Agents pin
// this key per CLI token.
func CLIKeyDir() string {
	if d := os.Getenv("MUSTER_CLOUD_KEY_DIR"); d != "" {
		return d
	}
	return filepath.Join(MusterHome(), "cloud_keys")
}

// KnownAgentsPath returns the file holding pinned agent public keys
// (~/.muster/cloud_known_agents.json).
func KnownAgentsPath() string {
	if p := os.Getenv("MUSTER_CLOUD_KNOWN_AGENTS"); p != "" {
		return p
	}
	return filepath.Join(MusterHome(), "cloud_known_agents.json")
}

// KnownAgents maps "<relay>|<org>|<agent>" to a base64 X25519 public key
// (trust on first use, like SSH known_hosts).
type KnownAgents map[string]string

// KnownAgentKey builds the KnownAgents map key.
func KnownAgentKey(relayWS, orgID, agent string) string {
	return relayWS + "|" + orgID + "|" + agent
}

// LoadKnownAgents reads the pin file; missing file → empty map.
func LoadKnownAgents(path string) (KnownAgents, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return KnownAgents{}, nil
	}
	if err != nil {
		return nil, err
	}
	k := KnownAgents{}
	if err := json.Unmarshal(data, &k); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return k, nil
}

// SaveKnownAgents atomically writes the pin file (0600).
func SaveKnownAgents(path string, k KnownAgents) error {
	data, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(data, '\n'), 0600)
}
