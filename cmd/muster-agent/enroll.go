package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/Muster-dev/muster-fleet-cloud/internal/agent"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/config"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/crypto"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/protocol"
)

// enrollParams are the inputs shared by `join` and `pair`.
type enrollParams struct {
	configPath string
	relayURL   string
	token      string // one-time join token (mst_agent_...)
	orgID      string // optional; checked against the token
	name       string
	projectDir string // "" keeps the existing one
	mode       string
	musterPath string // "" keeps the existing one
	keyPolicy  string // "" keeps the existing one
	quiet      bool   // suppress the agent's log lines (pair prints its own summary)
}

// enroll exchanges a join token for a node credential and saves the agent
// config (0600) next to its keypair.
func enroll(p enrollParams) (*protocol.AuthResponse, *crypto.KeyPair, error) {
	// Reuse an existing keypair so re-joining keeps the agent's identity key
	// (CLIs pin it); generate one otherwise.
	keyDir := config.KeyDirFor(p.configPath)
	keys, err := crypto.LoadKeyPair(keyDir)
	if err != nil {
		keys, err = crypto.GenerateKeyPair()
		if err != nil {
			return nil, nil, fmt.Errorf("generate keys: %w", err)
		}
		if err := crypto.SaveKeyPair(keyDir, keys); err != nil {
			return nil, nil, fmt.Errorf("save keys: %w", err)
		}
	}

	cfg := config.DefaultAgentConfig()
	if existing, err := config.LoadAgentConfigFrom(p.configPath); err == nil {
		cfg = existing // keep allowlist and other local settings
	}
	cfg.Relay.URL = p.relayURL
	cfg.Relay.Token = p.token
	cfg.Identity.OrgID = p.orgID
	cfg.Identity.Name = p.name
	if p.projectDir != "" {
		cfg.Project.Dir = p.projectDir
	}
	cfg.Project.Mode = p.mode
	if p.musterPath != "" {
		cfg.MusterPath = p.musterPath
	}
	if p.keyPolicy != "" {
		cfg.CLIKeyPolicy = p.keyPolicy
	}

	opts := agent.Options{ConfigPath: p.configPath, Version: version}
	if p.quiet {
		opts.Logf = func(string, ...any) {}
	}
	a, err := agent.New(cfg, keys, opts)
	if err != nil {
		return nil, nil, err
	}
	resp, err := a.Enroll(context.Background())
	if err != nil {
		return nil, nil, err
	}
	if resp.NodeToken == "" {
		return nil, nil, errors.New("relay did not issue a node credential")
	}
	return resp, keys, nil
}
