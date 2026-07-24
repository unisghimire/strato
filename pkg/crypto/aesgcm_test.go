package crypto

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSegSize = 1024 // small segments exercise boundaries cheaply

func encryptAll(t *testing.T, key, plaintext []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewStreamWriter(&buf, key, testSegSize)
	require.NoError(t, err)
	_, err = w.Write(plaintext)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func decryptAll(key, ciphertext []byte) ([]byte, error) {
	r, err := NewStreamReader(bytes.NewReader(ciphertext), key, testSegSize)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func TestStreamRoundTrip(t *testing.T) {
	key, err := NewDEK()
	require.NoError(t, err)

	sizes := []int{0, 1, testSegSize - 1, testSegSize, testSegSize + 1,
		3 * testSegSize, 3*testSegSize + 7}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			plaintext := make([]byte, size)
			_, err := rand.Read(plaintext)
			require.NoError(t, err)

			ct := encryptAll(t, key, plaintext)
			assert.Equal(t, EncryptedSize(int64(size), testSegSize), int64(len(ct)),
				"EncryptedSize must predict the exact stream length")

			got, err := decryptAll(key, ct)
			require.NoError(t, err)
			assert.Equal(t, plaintext, got)
		})
	}
}

func TestStreamRoundTripChunkedWrites(t *testing.T) {
	key, err := NewDEK()
	require.NoError(t, err)
	plaintext := make([]byte, 5*testSegSize+13)
	_, err = rand.Read(plaintext)
	require.NoError(t, err)

	// Write in awkward increments crossing segment boundaries.
	var buf bytes.Buffer
	w, err := NewStreamWriter(&buf, key, testSegSize)
	require.NoError(t, err)
	for i := 0; i < len(plaintext); i += 300 {
		end := min(i+300, len(plaintext))
		_, err := w.Write(plaintext[i:end])
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())

	got, err := decryptAll(key, buf.Bytes())
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

func TestStreamTamperDetection(t *testing.T) {
	key, err := NewDEK()
	require.NoError(t, err)
	plaintext := make([]byte, 2*testSegSize)
	_, err = rand.Read(plaintext)
	require.NoError(t, err)
	ct := encryptAll(t, key, plaintext)

	t.Run("bit flip in body", func(t *testing.T) {
		tampered := bytes.Clone(ct)
		tampered[len(tampered)/2] ^= 0x01
		_, err := decryptAll(key, tampered)
		assert.ErrorIs(t, err, ErrCiphertextCorrupt)
	})

	t.Run("truncated stream", func(t *testing.T) {
		_, err := decryptAll(key, ct[:len(ct)-20])
		assert.ErrorIs(t, err, ErrCiphertextCorrupt)
	})

	t.Run("truncated at segment boundary", func(t *testing.T) {
		// Cut off the entire final segment: without the final-flag design
		// this would silently succeed with partial plaintext.
		header := magicLen + noncePrefixLen
		firstSegLen := segHeaderLen + testSegSize + 16
		_, err := decryptAll(key, ct[:header+firstSegLen])
		assert.ErrorIs(t, err, ErrCiphertextCorrupt)
	})

	t.Run("wrong key", func(t *testing.T) {
		other, err := NewDEK()
		require.NoError(t, err)
		_, err = decryptAll(other, ct)
		assert.ErrorIs(t, err, ErrCiphertextCorrupt)
	})

	t.Run("garbage input", func(t *testing.T) {
		_, err := decryptAll(key, []byte("definitely not a stream"))
		assert.ErrorIs(t, err, ErrCiphertextCorrupt)
	})
}

func TestKeyWrapRoundTrip(t *testing.T) {
	kek, err := NewDEK()
	require.NoError(t, err)
	dek, err := NewDEK()
	require.NoError(t, err)

	wrapped, err := WrapKey(kek, dek)
	require.NoError(t, err)
	assert.NotEqual(t, dek, wrapped)

	got, err := UnwrapKey(kek, wrapped)
	require.NoError(t, err)
	assert.Equal(t, dek, got)

	t.Run("wrong KEK fails", func(t *testing.T) {
		otherKek, err := NewDEK()
		require.NoError(t, err)
		_, err = UnwrapKey(otherKek, wrapped)
		assert.ErrorIs(t, err, ErrCiphertextCorrupt)
	})

	t.Run("tampered wrap fails", func(t *testing.T) {
		bad := bytes.Clone(wrapped)
		bad[len(bad)-1] ^= 0xFF
		_, err := UnwrapKey(kek, bad)
		assert.ErrorIs(t, err, ErrCiphertextCorrupt)
	})
}

func BenchmarkStreamEncrypt(b *testing.B) {
	key, _ := NewDEK()
	plaintext := make([]byte, 8<<20) // 8 MiB
	_, _ = rand.Read(plaintext)

	b.SetBytes(int64(len(plaintext)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w, _ := NewStreamWriter(io.Discard, key, DefaultSegmentSize)
		_, _ = w.Write(plaintext)
		_ = w.Close()
	}
}

func BenchmarkStreamDecrypt(b *testing.B) {
	key, _ := NewDEK()
	plaintext := make([]byte, 8<<20)
	_, _ = rand.Read(plaintext)
	var buf bytes.Buffer
	w, _ := NewStreamWriter(&buf, key, DefaultSegmentSize)
	_, _ = w.Write(plaintext)
	_ = w.Close()
	ct := buf.Bytes()

	b.SetBytes(int64(len(plaintext)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, _ := NewStreamReader(bytes.NewReader(ct), key, DefaultSegmentSize)
		_, _ = io.Copy(io.Discard, r)
	}
}
