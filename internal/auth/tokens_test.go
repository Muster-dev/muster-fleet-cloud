package auth

import (
	"strings"
	"testing"
)

func TestGenerateTokenHasCorrectPrefix(t *testing.T) {
	prefixes := []struct {
		prefix string
		name   string
	}{
		{PrefixAdmin, "admin"},
		{PrefixAgentJoin, "agent_join"},
		{PrefixAgentSession, "agent_session"},
		{PrefixCLI, "cli"},
	}

	for _, tt := range prefixes {
		t.Run(tt.name, func(t *testing.T) {
			raw, _, err := GenerateToken(tt.prefix)
			if err != nil {
				t.Fatalf("GenerateToken(%q): %v", tt.prefix, err)
			}
			if !strings.HasPrefix(raw, tt.prefix) {
				t.Errorf("token %q does not start with prefix %q", raw, tt.prefix)
			}
		})
	}
}

func TestGenerateTokenProducesDifferentTokens(t *testing.T) {
	raw1, _, err := GenerateToken(PrefixAdmin)
	if err != nil {
		t.Fatalf("GenerateToken 1: %v", err)
	}
	raw2, _, err := GenerateToken(PrefixAdmin)
	if err != nil {
		t.Fatalf("GenerateToken 2: %v", err)
	}

	if raw1 == raw2 {
		t.Error("two generated tokens are identical")
	}
}

func TestHashTokenIsDeterministic(t *testing.T) {
	input := "mst_admin_abc123"
	h1 := HashToken(input)
	h2 := HashToken(input)

	if h1 != h2 {
		t.Errorf("HashToken not deterministic: got %q and %q", h1, h2)
	}
}

func TestHashTokenDifferentInputsDifferentOutputs(t *testing.T) {
	h1 := HashToken("mst_admin_token_aaa")
	h2 := HashToken("mst_admin_token_bbb")

	if h1 == h2 {
		t.Error("HashToken produced same output for different inputs")
	}
}

func TestHashTokenHasSHA256Prefix(t *testing.T) {
	h := HashToken("mst_admin_test")
	if !strings.HasPrefix(h, "sha256:") {
		t.Errorf("hash %q does not start with 'sha256:'", h)
	}
}

func TestTokenTypeFromPrefix(t *testing.T) {
	tests := []struct {
		raw      string
		expected TokenType
	}{
		{PrefixAdmin + "abc123", TypeAdmin},
		{PrefixAgentJoin + "abc123", TypeAgentJoin},
		{PrefixAgentSession + "abc123", TypeAgentSession},
		{PrefixCLI + "abc123", TypeCLI},
	}

	for _, tt := range tests {
		t.Run(string(tt.expected), func(t *testing.T) {
			got, err := TokenTypeFromPrefix(tt.raw)
			if err != nil {
				t.Fatalf("TokenTypeFromPrefix(%q): %v", tt.raw, err)
			}
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestTokenTypeFromPrefixUnknown(t *testing.T) {
	_, err := TokenTypeFromPrefix("unknown_prefix_token")
	if err == nil {
		t.Fatal("expected error for unknown prefix, got nil")
	}
}
