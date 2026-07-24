package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// RandomToken returns a URL-safe random token with n bytes of entropy.
// Used for refresh tokens and public share links; 32 bytes = 256 bits.
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken derives the storage digest for an opaque token. Tokens are
// high-entropy (not passwords), so a single unsalted SHA-256 is appropriate:
// it must be deterministic for lookup, and brute force is infeasible at
// 256 bits of entropy.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
