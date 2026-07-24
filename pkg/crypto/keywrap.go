package crypto

import (
	"crypto/rand"
	"fmt"
)

// Envelope encryption: every blob is encrypted with its own DEK, and the DEK
// is wrapped (encrypted) with the master KEK before being stored alongside
// blob metadata. The KEK never touches blob data, so rotating it means
// re-wrapping small DEKs rather than re-encrypting terabytes.

// WrapKey encrypts dek under kek using AES-256-GCM.
// Output layout: nonce (12B) || ciphertext+tag.
func WrapKey(kek, dek []byte) ([]byte, error) {
	aead, err := newGCM(kek)
	if err != nil {
		return nil, fmt.Errorf("wrap key: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("wrap key nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, dek, nil), nil
}

// UnwrapKey reverses WrapKey. It fails with ErrCiphertextCorrupt if the
// wrapped blob was tampered with or a different KEK is supplied.
func UnwrapKey(kek, wrapped []byte) ([]byte, error) {
	aead, err := newGCM(kek)
	if err != nil {
		return nil, fmt.Errorf("unwrap key: %w", err)
	}
	if len(wrapped) < aead.NonceSize() {
		return nil, ErrCiphertextCorrupt
	}
	nonce, ct := wrapped[:aead.NonceSize()], wrapped[aead.NonceSize():]
	dek, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrCiphertextCorrupt
	}
	return dek, nil
}
