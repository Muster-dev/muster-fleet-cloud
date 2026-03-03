package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/curve25519"
)

// KeyPair holds an X25519 key pair.
type KeyPair struct {
	PublicKey  [32]byte
	PrivateKey [32]byte
}

// GenerateKeyPair creates a new X25519 key pair.
func GenerateKeyPair() (*KeyPair, error) {
	var priv [32]byte
	if _, err := io.ReadFull(rand.Reader, priv[:]); err != nil {
		return nil, fmt.Errorf("generate random bytes: %w", err)
	}

	// Clamp private key per X25519 spec
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive public key: %w", err)
	}

	kp := &KeyPair{}
	copy(kp.PrivateKey[:], priv[:])
	copy(kp.PublicKey[:], pub)
	return kp, nil
}

// PublicKeyBase64 returns the public key as a base64 string.
func (kp *KeyPair) PublicKeyBase64() string {
	return base64.StdEncoding.EncodeToString(kp.PublicKey[:])
}

// PrivateKeyBase64 returns the private key as a base64 string.
func (kp *KeyPair) PrivateKeyBase64() string {
	return base64.StdEncoding.EncodeToString(kp.PrivateKey[:])
}

// DecodePublicKey decodes a base64-encoded public key.
func DecodePublicKey(b64 string) ([32]byte, error) {
	var key [32]byte
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return key, fmt.Errorf("decode base64: %w", err)
	}
	if len(raw) != 32 {
		return key, fmt.Errorf("invalid key length: %d (expected 32)", len(raw))
	}
	copy(key[:], raw)
	return key, nil
}

// SaveKeyPair writes keys to a directory with secure permissions.
func SaveKeyPair(dir string, kp *KeyPair) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}

	privPath := filepath.Join(dir, "identity.key")
	if err := os.WriteFile(privPath, kp.PrivateKey[:], 0600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}

	pubPath := filepath.Join(dir, "identity.pub")
	if err := os.WriteFile(pubPath, kp.PublicKey[:], 0644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}

	return nil
}

// LoadKeyPair reads keys from a directory.
func LoadKeyPair(dir string) (*KeyPair, error) {
	privPath := filepath.Join(dir, "identity.key")
	privBytes, err := os.ReadFile(privPath)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	if len(privBytes) != 32 {
		return nil, fmt.Errorf("invalid private key length: %d", len(privBytes))
	}

	pubPath := filepath.Join(dir, "identity.pub")
	pubBytes, err := os.ReadFile(pubPath)
	if err != nil {
		return nil, fmt.Errorf("read public key: %w", err)
	}
	if len(pubBytes) != 32 {
		return nil, fmt.Errorf("invalid public key length: %d", len(pubBytes))
	}

	kp := &KeyPair{}
	copy(kp.PrivateKey[:], privBytes)
	copy(kp.PublicKey[:], pubBytes)
	return kp, nil
}
