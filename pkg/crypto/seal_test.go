package crypto

import "testing"

func TestSealOpen(t *testing.T) {
	a, _ := GenerateKeyPair()
	b, _ := GenerateKeyPair()
	sealed, err := Seal([]byte("secret command"), &b.PublicKey, a)
	if err != nil {
		t.Fatal(err)
	}
	sender, pt, err := Open(sealed, b)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "secret command" || !KeysEqual(sender, a.PublicKey) {
		t.Fatalf("got %q from %x", pt, sender)
	}
	// Wrong recipient cannot open.
	c, _ := GenerateKeyPair()
	if _, _, err := Open(sealed, c); err == nil {
		t.Fatal("third party opened the box")
	}
	// Swapping the sender key prefix breaks authentication.
	tampered := append([]byte(nil), sealed...)
	copy(tampered[:32], c.PublicKey[:])
	if _, _, err := Open(tampered, b); err == nil {
		t.Fatal("tampered sender key accepted")
	}
	if _, ok := SealNonce(sealed); !ok {
		t.Fatal("SealNonce failed")
	}
	if Fingerprint(a.PublicKey) == Fingerprint(b.PublicKey) {
		t.Fatal("fingerprints collide")
	}
}
