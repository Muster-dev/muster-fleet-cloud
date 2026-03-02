package crypto

import (
	"crypto/rand"
	"fmt"
	"io"

	"golang.org/x/crypto/nacl/box"
)

const NonceSize = 24

// Encrypt seals plaintext using NaCl box (XSalsa20-Poly1305).
// Returns nonce + ciphertext concatenated.
func Encrypt(plaintext []byte, recipientPub, senderPriv *[32]byte) ([]byte, error) {
	var nonce [NonceSize]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	sealed := box.Seal(nonce[:], plaintext, &nonce, recipientPub, senderPriv)
	return sealed, nil
}

// Decrypt opens a NaCl box ciphertext. Input is nonce + ciphertext concatenated.
func Decrypt(sealed []byte, senderPub, recipientPriv *[32]byte) ([]byte, error) {
	if len(sealed) < NonceSize+box.Overhead {
		return nil, fmt.Errorf("ciphertext too short: %d bytes", len(sealed))
	}

	var nonce [NonceSize]byte
	copy(nonce[:], sealed[:NonceSize])

	plaintext, ok := box.Open(nil, sealed[NonceSize:], &nonce, senderPub, recipientPriv)
	if !ok {
		return nil, fmt.Errorf("decryption failed: invalid ciphertext or wrong keys")
	}

	return plaintext, nil
}

// EncryptWithNonce seals plaintext and returns nonce and ciphertext separately.
func EncryptWithNonce(plaintext []byte, recipientPub, senderPriv *[32]byte) (nonce [NonceSize]byte, ciphertext []byte, err error) {
	if _, err = io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nonce, nil, fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext = box.Seal(nil, plaintext, &nonce, recipientPub, senderPriv)
	return nonce, ciphertext, nil
}

// DecryptWithNonce opens a NaCl box with a separate nonce.
func DecryptWithNonce(ciphertext []byte, nonce *[NonceSize]byte, senderPub, recipientPriv *[32]byte) ([]byte, error) {
	plaintext, ok := box.Open(nil, ciphertext, nonce, senderPub, recipientPriv)
	if !ok {
		return nil, fmt.Errorf("decryption failed: invalid ciphertext or wrong keys")
	}
	return plaintext, nil
}
