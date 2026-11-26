package protocol

import (
	"regexp"
	"strings"
)

// Client types carried in AUTH_REQUEST.
const (
	ClientAgent = "agent"
	ClientCLI   = "cli"
)

// RelayID is the identity that addresses the relay itself.
const RelayID = "relay"

// AuthRequest is the JSON payload of an AUTH_REQUEST frame.
//
// The relay derives the org from the token; OrgID is optional and, when set,
// must match the token's org. For agents, Name is the requested machine name
// (only used when enrolling with a join token; a node token is bound to its
// name). For CLI clients the relay assigns a per-connection name.
type AuthRequest struct {
	Token      string `json:"token"`
	ClientType string `json:"client_type"`
	OrgID      string `json:"org_id,omitempty"`
	Name       string `json:"name,omitempty"`
	Version    string `json:"version,omitempty"`
}

// AuthResponse is the JSON payload of an AUTH_RESPONSE frame.
type AuthResponse struct {
	OK        bool   `json:"ok"`
	OrgID     string `json:"org_id,omitempty"`
	Name      string `json:"name,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// NodeToken is set only when an agent enrolled with a one-time join token.
	// The agent must persist it and use it for every later connection.
	NodeToken string `json:"node_token,omitempty"`

	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// AgentHello is the JSON payload of an AGENT_HELLO frame.
type AgentHello struct {
	AgentName string `json:"agent_name"`
	Version   string `json:"version"`
	PublicKey string `json:"public_key"`
	Hostname  string `json:"hostname,omitempty"`
	OS        string `json:"os,omitempty"`
	Arch      string `json:"arch,omitempty"`

	// Fields added in v1.2 (all optional; older relays ignore them).

	// Mode is the agent's project mode: "muster" or "push".
	Mode string `json:"mode,omitempty"`
	// ProjectDir reports whether a project directory is configured and
	// exists on the machine. The path itself is never sent.
	ProjectDir bool `json:"project_dir"`
	// AllowedCommands is the agent's exec allowlist (word-prefix entries).
	AllowedCommands []string `json:"allowed_commands,omitempty"`
	// AllowShell is true when the agent runs exec commands through a shell
	// without an allowlist.
	AllowShell bool `json:"allow_shell,omitempty"`
	// Features lists optional protocol features the agent supports
	// (FeatureCancel, FeatureCallerEnv, ...), so clients can tell a v1.2
	// agent from an older one.
	Features []string `json:"features,omitempty"`
}

// Optional agent features advertised in AgentHello.Features.
const (
	// FeatureCancel: the agent honours COMMAND_CANCEL and SESSION_CLOSED.
	FeatureCancel = "cancel"
	// FeatureCallerEnv: the agent sets MUSTER_VIA_AGENT / MUSTER_CLOUD_CALLER
	// / MUSTER_DEPLOY_SOURCE on every command.
	FeatureCallerEnv = "caller-env"
	// FeatureCLIKeyPinning: the agent pins CLI identity keys per token.
	FeatureCLIKeyPinning = "cli-key-pinning"
)

// SessionInfo is the JSON payload of a SESSION_INFO frame (relay -> agent).
// The relay sends it before the first frame it routes from a CLI connection
// to an agent, so the agent learns which credential that connection
// authenticated with. It comes from the relay, never from the CLI.
type SessionInfo struct {
	// CLI is the relay-assigned connection name (the SourceID of the CLI's
	// frames).
	CLI string `json:"cli"`
	// TokenID and TokenName identify the CLI token the connection
	// authenticated with.
	TokenID   string `json:"token_id"`
	TokenName string `json:"token_name,omitempty"`
}

// SessionClosed is the JSON payload of a SESSION_CLOSED frame (relay ->
// agent): the CLI connection is gone, so its commands should be stopped.
type SessionClosed struct {
	CLI string `json:"cli"`
}

// ErrorPayload is the JSON payload of ERROR and COMMAND_ERROR frames.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// KeyExchange is the JSON payload of KEY_EXCHANGE and KEY_EXCHANGE_ACK frames.
type KeyExchange struct {
	PublicKey string `json:"public_key"`
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// ValidName reports whether s is a valid agent/CLI name. Names are what
// travel in the 32-byte SourceID/DestID header fields, so they are limited to
// 32 bytes of [A-Za-z0-9._-], starting with an alphanumeric.
func ValidName(s string) bool {
	return nameRe.MatchString(s) && s != RelayID
}

// SanitizeName turns an arbitrary string (e.g. a hostname) into a valid name,
// or returns "" if nothing usable remains.
func SanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	if len(out) > 32 {
		out = strings.TrimRight(out[:32], "-._")
	}
	if !ValidName(out) {
		return ""
	}
	return out
}

// NameFromID returns the name part of a header identity. Identities are bare
// names in protocol v1.1; legacy clients sent "<org>/<name>", in which case
// the part after the last "/" is returned. The org part is never trusted —
// the relay always routes within the sender's authenticated org.
func NameFromID(id string) string {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		return id[i+1:]
	}
	return id
}
