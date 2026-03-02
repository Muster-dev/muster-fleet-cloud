package crypto

import (
	"bytes"
	"testing"
)

func TestGenerateKeyPairProducesDifferentKeys(t *testing.T) {
	kp1, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair 1: %v", err)
	}
	kp2, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair 2: %v", err)
	}

	if kp1.PublicKey == kp2.PublicKey {
		t.Error("two generated key pairs have identical public keys")
	}
	if kp1.PrivateKey == kp2.PrivateKey {
		t.Error("two generated key pairs have identical private keys")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	sender, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipient, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	plaintext := []byte("muster deploy --service api")
	sealed, err := Encrypt(plaintext, &recipient.PublicKey, &sender.PrivateKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	decrypted, err := Decrypt(sealed, &sender.PublicKey, &recipient.PrivateKey)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("decrypted text mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestDecryptWithWrongKeyFails(t *testing.T) {
	sender, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipient, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	wrongKey, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate wrong key: %v", err)
	}

	plaintext := []byte("secret data")
	sealed, err := Encrypt(plaintext, &recipient.PublicKey, &sender.PrivateKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	_, err = Decrypt(sealed, &sender.PublicKey, &wrongKey.PrivateKey)
	if err == nil {
		t.Fatal("expected decryption to fail with wrong key, got nil error")
	}
}

func TestEmptyPlaintextEncryptDecrypt(t *testing.T) {
	sender, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipient, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	plaintext := []byte{}
	sealed, err := Encrypt(plaintext, &recipient.PublicKey, &sender.PrivateKey)
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}

	decrypted, err := Decrypt(sealed, &sender.PublicKey, &recipient.PrivateKey)
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}

	if len(decrypted) != 0 {
		t.Errorf("expected empty plaintext, got %d bytes", len(decrypted))
	}
}

func TestPublicKeyBase64DecodeRoundTrip(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	b64 := kp.PublicKeyBase64()
	decoded, err := DecodePublicKey(b64)
	if err != nil {
		t.Fatalf("DecodePublicKey: %v", err)
	}

	if decoded != kp.PublicKey {
		t.Errorf("PublicKeyBase64/DecodePublicKey round-trip mismatch")
	}
}

func TestSaveLoadKeyPairRoundTrip(t *testing.T) {
	dir := t.TempDir()

	original, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	if err := SaveKeyPair(dir, original); err != nil {
		t.Fatalf("SaveKeyPair: %v", err)
	}

	loaded, err := LoadKeyPair(dir)
	if err != nil {
		t.Fatalf("LoadKeyPair: %v", err)
	}

	if loaded.PublicKey != original.PublicKey {
		t.Error("loaded public key does not match original")
	}
	if loaded.PrivateKey != original.PrivateKey {
		t.Error("loaded private key does not match original")
	}
}
