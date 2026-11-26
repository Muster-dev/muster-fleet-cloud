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
