package crypto

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fast parameters for tests: correctness is parameter-independent.
var testParams = Argon2Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple", testParams)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "$argon2id$v=19$"))

	ok, err := VerifyPassword("correct horse battery staple", hash)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = VerifyPassword("wrong password", hash)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestHashesAreSalted(t *testing.T) {
	h1, err := HashPassword("same password", testParams)
	require.NoError(t, err)
	h2, err := HashPassword("same password", testParams)
	require.NoError(t, err)
	assert.NotEqual(t, h1, h2, "two hashes of one password must differ (random salt)")
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	for _, bad := range []string{
		"",
		"not a hash",
		"$argon2i$v=19$m=8,t=1,p=1$c2FsdA$aGFzaA", // wrong variant
		"$argon2id$v=19$m=8,t=1,p=1$!!!$aGFzaA",   // bad base64
	} {
		_, err := VerifyPassword("x", bad)
		assert.ErrorIs(t, err, ErrInvalidHash, "input: %q", bad)
	}
}

func TestNeedsRehash(t *testing.T) {
	weak := Argon2Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	strong := Argon2Params{Memory: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32}

	hash, err := HashPassword("password12", weak)
	require.NoError(t, err)

	assert.True(t, NeedsRehash(hash, strong), "weak hash should need rehash under strong policy")
	assert.False(t, NeedsRehash(hash, weak), "hash at policy should not need rehash")
	assert.True(t, NeedsRehash("garbage", strong), "unparseable hash should be replaced")
}

func TestTokenHashing(t *testing.T) {
	token, err := RandomToken(32)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(token), 43) // 32 bytes base64url

	h1 := HashToken(token)
	h2 := HashToken(token)
	assert.Equal(t, h1, h2, "token hashing must be deterministic")
	assert.Len(t, h1, 32)

	other, err := RandomToken(32)
	require.NoError(t, err)
	assert.NotEqual(t, HashToken(token), HashToken(other))
}

func BenchmarkHashPassword(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = HashPassword("benchmark password", DefaultArgon2Params)
	}
}
