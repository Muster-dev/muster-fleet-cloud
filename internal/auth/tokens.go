package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Token prefixes identify token types.
const (
	PrefixAdmin        = "mst_admin_"
	PrefixAgentJoin    = "mst_agent_"
	PrefixAgentSession = "mst_asess_"
	PrefixCLI          = "mst_cli_"
)

// TokenType represents the type of a token.
type TokenType string

const (
	TypeAdmin        TokenType = "admin"
	TypeAgentJoin    TokenType = "agent_join"
	TypeAgentSession TokenType = "agent_session"
	TypeCLI          TokenType = "cli"
)

// Token represents a stored token record.
type Token struct {
	ID         string    `json:"id"`
	Hash       string    `json:"hash"` // sha256:hex
	OrgID      string    `json:"org_id"`
	TokenType  TokenType `json:"token_type"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
	Used       bool      `json:"used,omitempty"` // for one-time join tokens
}

// GenerateToken creates a new token with the given prefix.
func GenerateToken(prefix string) (raw string, hash string, err error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", fmt.Errorf("generate random bytes: %w", err)
	}

	raw = prefix + hex.EncodeToString(bytes)
	hash = HashToken(raw)
	return raw, hash, nil
}

// HashToken returns the SHA-256 hash of a token.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TokenTypeFromPrefix determines the token type from its prefix.
func TokenTypeFromPrefix(raw string) (TokenType, error) {
	switch {
	case strings.HasPrefix(raw, PrefixAdmin):
		return TypeAdmin, nil
	case strings.HasPrefix(raw, PrefixAgentJoin):
		return TypeAgentJoin, nil
	case strings.HasPrefix(raw, PrefixAgentSession):
		return TypeAgentSession, nil
	case strings.HasPrefix(raw, PrefixCLI):
		return TypeCLI, nil
	default:
		return "", fmt.Errorf("unknown token prefix")
	}
}
