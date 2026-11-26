package crypto

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"

	"golang.org/x/crypto/nacl/box"
)

// SealedOverhead is the number of bytes Seal adds to a plaintext:
// 32-byte sender public key + 24-byte nonce + 16-byte Poly1305 tag.
const SealedOverhead = 32 + NonceSize + box.Overhead

// Seal encrypts plaintext from kp to recipientPub and returns the MTP
// encrypted payload: senderPublicKey(32) || nonce(24) || box(ciphertext).
//
// Carrying the sender's public key lets the recipient answer without any
// per-session state, and lets the recipient check the sender against a
// pinned key.
func Seal(plaintext []byte, recipientPub *[32]byte, kp *KeyPair) ([]byte, error) {
	sealed, err := Encrypt(plaintext, recipientPub, &kp.PrivateKey)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 32+len(sealed))
	out = append(out, kp.PublicKey[:]...)
	out = append(out, sealed...)
	return out, nil
}

// Open decrypts an MTP encrypted payload addressed to kp. It returns the
// sender's public key (authenticated by the box MAC) and the plaintext.
func Open(payload []byte, kp *KeyPair) (senderPub [32]byte, plaintext []byte, err error) {
	if len(payload) < SealedOverhead {
		return senderPub, nil, fmt.Errorf("encrypted payload too short: %d bytes", len(payload))
	}
	copy(senderPub[:], payload[:32])
	plaintext, err = Decrypt(payload[32:], &senderPub, &kp.PrivateKey)
	if err != nil {
		return senderPub, nil, err
	}
	return senderPub, plaintext, nil
}

// SealNonce returns the 24-byte nonce of an encrypted payload (useful for
// replay detection). ok is false if the payload is too short.
func SealNonce(payload []byte) (nonce [NonceSize]byte, ok bool) {
	if len(payload) < SealedOverhead {
		return nonce, false
	}
	copy(nonce[:], payload[32:32+NonceSize])
	return nonce, true
}

// KeysEqual compares two public keys in constant time.
func KeysEqual(a, b [32]byte) bool {
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// Fingerprint returns a short human-readable fingerprint of a public key
// (first 16 bytes of its SHA-256, hex, grouped) for display.
func Fingerprint(pub [32]byte) string {
	const hexdigits = "0123456789abcdef"
	sum := sha256.Sum256(pub[:])
	b := make([]byte, 0, 16*3)
	for i := 0; i < 16; i++ {
		if i > 0 && i%2 == 0 {
			b = append(b, ':')
		}
		b = append(b, hexdigits[sum[i]>>4], hexdigits[sum[i]&0x0f])
	}
	return string(b)
}
