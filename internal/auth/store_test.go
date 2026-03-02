package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func tempStorePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "tokens.json")
}

func TestNewTokenStoreCreatesEmpty(t *testing.T) {
	path := tempStorePath(t)
	store, err := NewTokenStore(path)
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}
	tokens := store.ListTokens("")
	if len(tokens) != 0 {
		t.Errorf("expected 0 tokens, got %d", len(tokens))
	}
}

func TestCreateAndValidateToken(t *testing.T) {
	path := tempStorePath(t)
	store, err := NewTokenStore(path)
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}

	raw, hash, err := GenerateToken(PrefixAdmin)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	id, err := store.CreateToken(hash, TypeAdmin, "acme", "test-admin")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty token ID")
	}

	tok, err := store.ValidateToken(raw)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if tok.OrgID != "acme" {
		t.Errorf("expected org_id 'acme', got %q", tok.OrgID)
	}
	if tok.TokenType != TypeAdmin {
		t.Errorf("expected type 'admin', got %q", tok.TokenType)
	}
}

func TestValidateTokenInvalid(t *testing.T) {
	path := tempStorePath(t)
	store, err := NewTokenStore(path)
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}

	_, err = store.ValidateToken("mst_admin_bogus")
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
}

func TestMarkUsed(t *testing.T) {
	path := tempStorePath(t)
	store, err := NewTokenStore(path)
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}

	raw, hash, _ := GenerateToken(PrefixAgentJoin)
	id, _ := store.CreateToken(hash, TypeAgentJoin, "acme", "join-1")

	// First validate should work
	_, err = store.ValidateToken(raw)
	if err != nil {
		t.Fatalf("first ValidateToken: %v", err)
	}

	// Mark as used
	if err := store.MarkUsed(id); err != nil {
		t.Fatalf("MarkUsed: %v", err)
	}

	// Second validate should fail
	_, err = store.ValidateToken(raw)
	if err == nil {
		t.Fatal("expected error for used token")
	}
}

func TestRevokeToken(t *testing.T) {
	path := tempStorePath(t)
	store, err := NewTokenStore(path)
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}

	raw, hash, _ := GenerateToken(PrefixCLI)
	id, _ := store.CreateToken(hash, TypeCLI, "acme", "cli-1")

	if err := store.RevokeToken(id); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	_, err = store.ValidateToken(raw)
	if err == nil {
		t.Fatal("expected error for revoked token")
	}

	tokens := store.ListTokens("")
	if len(tokens) != 0 {
		t.Errorf("expected 0 tokens after revoke, got %d", len(tokens))
	}
}

func TestRevokeTokenNotFound(t *testing.T) {
	path := tempStorePath(t)
	store, _ := NewTokenStore(path)

	err := store.RevokeToken("tok_nonexistent")
	if err == nil {
		t.Fatal("expected error for revoking nonexistent token")
	}
}

func TestListTokensFiltersByOrg(t *testing.T) {
	path := tempStorePath(t)
	store, _ := NewTokenStore(path)

	_, h1, _ := GenerateToken(PrefixAdmin)
	_, h2, _ := GenerateToken(PrefixAdmin)
	store.CreateToken(h1, TypeAdmin, "acme", "admin-1")
	store.CreateToken(h2, TypeAdmin, "globex", "admin-2")

	acmeTokens := store.ListTokens("acme")
	if len(acmeTokens) != 1 {
		t.Errorf("expected 1 acme token, got %d", len(acmeTokens))
	}

	allTokens := store.ListTokens("")
	if len(allTokens) != 2 {
		t.Errorf("expected 2 total tokens, got %d", len(allTokens))
	}
}

func TestStorePersistence(t *testing.T) {
	path := tempStorePath(t)

	// Create store and add token
	store1, _ := NewTokenStore(path)
	raw, hash, _ := GenerateToken(PrefixAdmin)
	store1.CreateToken(hash, TypeAdmin, "acme", "persist-test")

	// Reopen store and validate
	store2, err := NewTokenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}

	tok, err := store2.ValidateToken(raw)
	if err != nil {
		t.Fatalf("validate after reopen: %v", err)
	}
	if tok.Name != "persist-test" {
		t.Errorf("expected name 'persist-test', got %q", tok.Name)
	}
}

func TestStoreFilePermissions(t *testing.T) {
	path := tempStorePath(t)
	store, _ := NewTokenStore(path)

	_, hash, _ := GenerateToken(PrefixAdmin)
	store.CreateToken(hash, TypeAdmin, "acme", "perm-test")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat store file: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected file permissions 0600, got %o", perm)
	}
}

func TestMarkUsedNotFound(t *testing.T) {
	path := tempStorePath(t)
	store, _ := NewTokenStore(path)

	err := store.MarkUsed("tok_nonexistent")
	if err == nil {
		t.Fatal("expected error for marking nonexistent token")
	}
}
