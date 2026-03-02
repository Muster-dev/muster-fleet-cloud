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
	LogFile         string   `json:"log_file,omitempty"`

	ReconnectBaseDelay string `json:"reconnect_base_delay,omitempty"`
	ReconnectMaxDelay  string `json:"reconnect_max_delay,omitempty"`
}

type RelayConfig struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

type IdentityConfig struct {
	OrgID string `json:"org_id"`
	Name  string `json:"name"`
}

type ProjectConfig struct {
	Dir  string `json:"dir"`
	Mode string `json:"mode"` // "muster" or "push"
}

// RelayServerConfig is the muster-cloud relay server configuration.
type RelayServerConfig struct {
	Listen   string        `json:"listen"`
	TLS      TLSConfig     `json:"tls,omitempty"`
	Database DatabaseConfig `json:"database"`

	HeartbeatInterval string `json:"heartbeat_interval,omitempty"`
	HeartbeatTimeout  string `json:"heartbeat_timeout,omitempty"`

	LogLevel  string `json:"log_level,omitempty"`
	LogFormat string `json:"log_format,omitempty"`
}

type TLSConfig struct {
	Cert string `json:"cert,omitempty"`
	Key  string `json:"key,omitempty"`
}

type DatabaseConfig struct {
	Path string `json:"path"` // SQLite file path
}

// DefaultAgentConfig returns sensible defaults for the agent.
func DefaultAgentConfig() *AgentConfig {
	return &AgentConfig{
		MusterPath: "muster",
		AllowedCommands: []string{
			"muster deploy",
			"muster status",
			"muster rollback",
			"muster logs",
		},
		ReconnectBaseDelay: "1s",
		ReconnectMaxDelay:  "60s",
	}
}

// DefaultRelayConfig returns sensible defaults for the relay.
func DefaultRelayConfig() *RelayServerConfig {
	return &RelayServerConfig{
		Listen:            ":8443",
		HeartbeatInterval: "30s",
		HeartbeatTimeout:  "90s",
		LogLevel:          "info",
		LogFormat:         "json",
		Database: DatabaseConfig{
			Path: "muster-cloud.db",
		},
	}
}

// AgentConfigDir returns the agent config directory path.
func AgentConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".muster-agent")
}

// AgentConfigPath returns the agent config file path.
func AgentConfigPath() string {
	return filepath.Join(AgentConfigDir(), "config.json")
}

// LoadAgentConfig reads the agent config from disk.
func LoadAgentConfig() (*AgentConfig, error) {
	path := AgentConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := DefaultAgentConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// SaveAgentConfig writes the agent config to disk with secure permissions.
func SaveAgentConfig(cfg *AgentConfig) error {
	dir := AgentConfigDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	path := AgentConfigPath()
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// LoadRelayConfig reads the relay config from a file path.
func LoadRelayConfig(path string) (*RelayServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := DefaultRelayConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}
