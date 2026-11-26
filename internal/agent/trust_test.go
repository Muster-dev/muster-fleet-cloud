package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Muster-dev/muster-fleet-cloud/pkg/config"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/crypto"
)

func TestTrustStoreTOFU(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trusted_clis.json")
	ts := NewTrustStore(path)
	k1, _ := crypto.GenerateKeyPair()
	k2, _ := crypto.GenerateKeyPair()

	// Ephemeral keys of an unpinned token (v0.3 CLIs) pass without pinning.
	if d, err := ts.Check("", "tok_a", "a", k1.PublicKey, false); err != nil || d != TrustUnpinned {
		t.Fatalf("ephemeral: %v %v", d, err)
	}
	if d, err := ts.Check("", "tok_a", "a", k1.PublicKey, true); err != nil || d != TrustPinned {
		t.Fatalf("first persistent: %v %v", d, err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("pin file: %v %v", st, err)
	}
	if d, err := ts.Check("tofu", "tok_a", "a", k1.PublicKey, true); err != nil || d != TrustAllowed {
		t.Fatalf("pinned key: %v %v", d, err)
	}
	if _, err := ts.Check("tofu", "tok_a", "a", k2.PublicKey, true); err == nil {
		t.Fatal("changed key accepted")
	}
	if _, err := ts.Check("tofu", "tok_a", "a", k2.PublicKey, false); err == nil {
		t.Fatal("ephemeral key accepted for a pinned token")
	}
	// Other tokens are independent; unknown token ID (old relay) passes.
	if _, err := ts.Check("tofu", "tok_b", "b", k2.PublicKey, true); err != nil {
		t.Fatal(err)
	}
	if d, err := ts.Check("tofu", "", "", k2.PublicKey, true); err != nil || d != TrustUnpinned {
		t.Fatalf("unidentified: %v %v", d, err)
	}
	// Off disables everything.
	if _, err := ts.Check(config.CLIKeyPolicyOff, "tok_a", "a", k2.PublicKey, true); err != nil {
		t.Fatal(err)
	}

	// A second store on the same file (the `trust` CLI) sees and edits pins.
	other := NewTrustStore(path)
	list, err := other.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}
	if n, err := other.Reset("tok_a"); err != nil || n != 1 {
		t.Fatalf("reset: %d %v", n, err)
	}
	if d, err := ts.Check("tofu", "tok_a", "a", k2.PublicKey, true); err != nil || d != TrustPinned {
		t.Fatalf("after reset: %v %v", d, err)
	}
	if n, _ := other.Reset(""); n != 2 {
		t.Fatalf("reset all removed %d", n)
	}
}

func TestTrustStoreStrict(t *testing.T) {
	ts := NewTrustStore("")
	k, _ := crypto.GenerateKeyPair()
	if _, err := ts.Check("strict", "tok_a", "a", k.PublicKey, true); err == nil {
		t.Fatal("strict accepted an unknown token")
	}
	if _, err := ts.Check("strict", "", "", k.PublicKey, true); err == nil {
		t.Fatal("strict accepted an unidentified caller")
	}
	if err := ts.Add("tok_a", "a", k.PublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Check("strict", "tok_a", "a", k.PublicKey, false); err != nil {
		t.Fatal(err)
	}
}
