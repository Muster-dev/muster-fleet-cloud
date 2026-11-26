package agent

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/Muster-dev/muster-fleet-cloud/pkg/config"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/crypto"
)

// TrustedCLI is a pinned CLI identity key for one CLI token.
type TrustedCLI struct {
	TokenID   string    `json:"token_id"`
	TokenName string    `json:"token_name,omitempty"`
	Key       string    `json:"key"` // base64 X25519 public key
	PinnedAt  time.Time `json:"pinned_at"`
	// Source is "tofu" (pinned on first use) or "manual" (trust add).
	Source string `json:"source"`
}

// TrustStore pins CLI identity keys per CLI token ID (trusted_clis.json next
// to the agent config, mode 0600).
//
// The file is re-read on every check, so `muster-agent trust reset/add`
// run from another process takes effect on a running agent immediately.
type TrustStore struct {
	mu   sync.Mutex
	path string // "" = in memory only
	mem  map[string]TrustedCLI
}

// NewTrustStore returns a store backed by path ("" keeps pins in memory).
func NewTrustStore(path string) *TrustStore {
	return &TrustStore{path: path, mem: map[string]TrustedCLI{}}
}

func (t *TrustStore) load() (map[string]TrustedCLI, error) {
	if t.path == "" {
		return t.mem, nil
	}
	data, err := os.ReadFile(t.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]TrustedCLI{}, nil
	}
	if err != nil {
		return nil, err
	}
	var list []TrustedCLI
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse %s: %w", t.path, err)
	}
	m := make(map[string]TrustedCLI, len(list))
	for _, e := range list {
		m[e.TokenID] = e
	}
	return m, nil
}

func (t *TrustStore) save(m map[string]TrustedCLI) error {
	if t.path == "" {
		t.mem = m
		return nil
	}
	list := make([]TrustedCLI, 0, len(m))
	for _, e := range m {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].TokenID < list[j].TokenID })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(t.path, append(data, '\n'), 0600)
}

// List returns the pinned keys sorted by token ID.
func (t *TrustStore) List() ([]TrustedCLI, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, err := t.load()
	if err != nil {
		return nil, err
	}
	out := make([]TrustedCLI, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TokenID < out[j].TokenID })
	return out, nil
}

// Add pins key for tokenID (replacing any existing pin).
func (t *TrustStore) Add(tokenID, tokenName string, key [32]byte) error {
	if tokenID == "" {
		return errors.New("token ID is required")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	m, err := t.load()
	if err != nil {
		return err
	}
	m[tokenID] = TrustedCLI{TokenID: tokenID, TokenName: tokenName, Key: base64.StdEncoding.EncodeToString(key[:]), PinnedAt: time.Now().UTC(), Source: "manual"}
	return t.save(m)
}

// Reset forgets the pin for tokenID ("" forgets all). It reports how many
// pins were removed.
func (t *TrustStore) Reset(tokenID string) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, err := t.load()
	if err != nil {
		return 0, err
	}
	n := 0
	if tokenID == "" {
		n = len(m)
		m = map[string]TrustedCLI{}
	} else if _, ok := m[tokenID]; ok {
		delete(m, tokenID)
		n = 1
	}
	if n == 0 {
		return 0, nil
	}
	return n, t.save(m)
}

// TrustDecision explains the outcome of Check.
type TrustDecision int

const (
	TrustAllowed  TrustDecision = iota // key matches a pin, or policy is off
	TrustPinned                        // first persistent key for the token: pinned now
	TrustUnpinned                      // allowed without a pin (ephemeral key or unknown token, tofu)
)

// Check applies policy to a command sealed with key, sent by a CLI that the
// relay says authenticated with tokenID. persistent is the CLI's claim that
// key is its long-lived identity key.
//
// tofu: a pinned token must use its pinned key; an unpinned token's first
// persistent key is pinned; ephemeral keys of unpinned tokens (v0.3 CLIs)
// are allowed. strict: only pinned token+key pairs are allowed. off: always
// allowed.
func (t *TrustStore) Check(policy, tokenID, tokenName string, key [32]byte, persistent bool) (TrustDecision, error) {
	if policy == config.CLIKeyPolicyOff {
		return TrustAllowed, nil
	}
	strict := policy == config.CLIKeyPolicyStrict
	if tokenID == "" {
		if strict {
			return 0, errors.New("the relay did not identify the calling CLI token (relay too old?); cli_key_policy is strict")
		}
		return TrustUnpinned, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	m, err := t.load()
	if err != nil {
		return 0, fmt.Errorf("read trusted CLI keys: %w", err)
	}
	b64 := base64.StdEncoding.EncodeToString(key[:])
	if pin, ok := m[tokenID]; ok {
		if pin.Key == b64 {
			return TrustAllowed, nil
		}
		old, _ := crypto.DecodePublicKey(pin.Key)
		return 0, fmt.Errorf("CLI key for token %s changed (pinned %s, got %s); if that CLI was re-installed, run 'muster-agent trust reset %s' on this machine",
			tokenID, crypto.Fingerprint(old), crypto.Fingerprint(key), tokenID)
	}
	if strict {
		return 0, fmt.Errorf("CLI token %s (key %s) is not trusted; cli_key_policy is strict: run 'muster-agent trust add %s %s' on this machine",
			tokenID, crypto.Fingerprint(key), tokenID, b64)
	}
	if !persistent {
		return TrustUnpinned, nil
	}
	m[tokenID] = TrustedCLI{TokenID: tokenID, TokenName: tokenName, Key: b64, PinnedAt: time.Now().UTC(), Source: "tofu"}
	if err := t.save(m); err != nil {
		return 0, fmt.Errorf("save trusted CLI key: %w", err)
	}
	return TrustPinned, nil
}
