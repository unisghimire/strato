// Package crypto provides the primitives Strato relies on: streaming
// AES-256-GCM (STREAM-style segmented encryption), envelope key wrapping,
// Argon2id password hashing, and secure random token generation.
package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Segmented AES-256-GCM streaming, modeled after the STREAM construction
// (Hoang/Reyhanitabar/Rogaway/Vizár) as used by Tink and Miscreant.
//
// Why not one giant GCM call: GCM requires the full plaintext in memory and
// caps out at ~64 GiB per nonce. Segmenting gives constant-memory streaming
// for arbitrarily large files, and the final-segment flag defeats truncation
// attacks (an attacker cutting the stream short is detected because the last
// segment must carry the final marker inside authenticated data).
//
// Wire format:
//
//	header : magic "SGC1" (4B) || noncePrefix (8B)
//	segment: ctLen uint32 BE (4B) || final flag (1B) || ciphertext (ctLen)
//
// Per-segment nonce = noncePrefix (8B) || segment counter uint32 BE (4B).
// The final flag byte is bound as GCM additional data, so neither the flag
// nor segment reordering (counter is in the nonce) can be forged.

const (
	// DefaultSegmentSize is the plaintext size per GCM segment. 64 KiB keeps
	// memory bounded while amortizing the 16-byte tag overhead to ~0.02%.
	DefaultSegmentSize = 64 * 1024

	// KeySize is the AES-256 key length in bytes.
	KeySize = 32

	noncePrefixLen = 8
	nonceLen       = 12
	magicLen       = 4
	segHeaderLen   = 5 // uint32 length + final flag byte

	flagMore  byte = 0
	flagFinal byte = 1
)

var streamMagic = []byte("SGC1")

// ErrCiphertextCorrupt indicates authentication failure, truncation, or a
// malformed stream. It deliberately carries no detail about which.
var ErrCiphertextCorrupt = errors.New("crypto: ciphertext corrupt or truncated")

// NewDEK generates a fresh random 256-bit data-encryption key.
func NewDEK() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating DEK: %w", err)
	}
	return key, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("crypto: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// StreamWriter encrypts written plaintext into an underlying writer using
// segmented AES-256-GCM. Close MUST be called to flush the final segment;
// omitting it produces a stream that fails authentication on read.
type StreamWriter struct {
	dst         io.Writer
	aead        cipher.AEAD
	noncePrefix [noncePrefixLen]byte
	buf         []byte
	segSize     int
	counter     uint32
	closed      bool
	err         error
}

// NewStreamWriter creates an encrypting writer. segmentSize <= 0 selects
// DefaultSegmentSize. The stream header is written on the first Write (or
// Close, for empty payloads).
func NewStreamWriter(dst io.Writer, key []byte, segmentSize int) (*StreamWriter, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if segmentSize <= 0 {
		segmentSize = DefaultSegmentSize
	}
	w := &StreamWriter{dst: dst, aead: aead, segSize: segmentSize, buf: make([]byte, 0, segmentSize)}
	if _, err := rand.Read(w.noncePrefix[:]); err != nil {
		return nil, fmt.Errorf("generating nonce prefix: %w", err)
	}
	if _, err := dst.Write(streamMagic); err != nil {
		return nil, fmt.Errorf("writing stream header: %w", err)
	}
	if _, err := dst.Write(w.noncePrefix[:]); err != nil {
		return nil, fmt.Errorf("writing stream header: %w", err)
	}
	return w, nil
}

// Write implements io.Writer. Full segments are encrypted and flushed as
// non-final; a partial segment is retained until more data arrives or Close.
func (w *StreamWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.closed {
		return 0, errors.New("crypto: write after Close")
	}
	total := len(p)
	for len(p) > 0 {
		room := w.segSize - len(w.buf)
		n := min(room, len(p))
		w.buf = append(w.buf, p[:n]...)
		p = p[n:]
		// Only flush when we know more plaintext follows: a full buffer with
		// remaining input is definitively non-final.
		if len(w.buf) == w.segSize && len(p) > 0 {
			if err := w.flushSegment(flagMore); err != nil {
				return total - len(p), err
			}
		}
	}
	return total, nil
}

// Close flushes the buffered plaintext as the final authenticated segment.
func (w *StreamWriter) Close() error {
	if w.err != nil {
		return w.err
	}
	if w.closed {
		return nil
	}
	w.closed = true
	return w.flushSegment(flagFinal)
}

func (w *StreamWriter) flushSegment(flag byte) error {
	var nonce [nonceLen]byte
	copy(nonce[:], w.noncePrefix[:])
	binary.BigEndian.PutUint32(nonce[noncePrefixLen:], w.counter)
	w.counter++

	ct := w.aead.Seal(nil, nonce[:], w.buf, []byte{flag})
	w.buf = w.buf[:0]

	var hdr [segHeaderLen]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(ct))) //nolint:gosec // segment size is bounded
	hdr[4] = flag
	if _, err := w.dst.Write(hdr[:]); err != nil {
		w.err = fmt.Errorf("writing segment header: %w", err)
		return w.err
	}
	if _, err := w.dst.Write(ct); err != nil {
		w.err = fmt.Errorf("writing segment: %w", err)
		return w.err
	}
	return nil
}

// StreamReader decrypts a stream produced by StreamWriter. It authenticates
// every segment and fails with ErrCiphertextCorrupt on tampering, reordering,
// or truncation (a stream that ends without a final segment).
type StreamReader struct {
	src         io.Reader
	aead        cipher.AEAD
	noncePrefix [noncePrefixLen]byte
	plain       bytes.Reader
	counter     uint32
	maxSegment  int
	sawFinal    bool
	err         error
}

// NewStreamReader creates a decrypting reader. segmentSize must be >= the
// value used at encryption time (it bounds per-segment allocation);
// <= 0 selects DefaultSegmentSize.
func NewStreamReader(src io.Reader, key []byte, segmentSize int) (*StreamReader, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if segmentSize <= 0 {
		segmentSize = DefaultSegmentSize
	}
	r := &StreamReader{src: src, aead: aead, maxSegment: segmentSize}

	var magic [magicLen]byte
	if _, err := io.ReadFull(src, magic[:]); err != nil {
		return nil, ErrCiphertextCorrupt
	}
	if !bytes.Equal(magic[:], streamMagic) {
		return nil, ErrCiphertextCorrupt
	}
	if _, err := io.ReadFull(src, r.noncePrefix[:]); err != nil {
		return nil, ErrCiphertextCorrupt
	}
	return r, nil
}

// Read implements io.Reader, decrypting segment by segment.
func (r *StreamReader) Read(p []byte) (int, error) {
	for {
		if r.plain.Len() > 0 {
			return r.plain.Read(p)
		}
		if r.err != nil {
			return 0, r.err
		}
		if err := r.nextSegment(); err != nil {
			r.err = err
			if r.plain.Len() > 0 {
				continue // serve remaining plaintext before surfacing EOF
			}
			return 0, err
		}
	}
}

func (r *StreamReader) nextSegment() error {
	if r.sawFinal {
		return io.EOF
	}
	var hdr [segHeaderLen]byte
	if _, err := io.ReadFull(r.src, hdr[:]); err != nil {
		// Any EOF before the final-flagged segment is truncation.
		return ErrCiphertextCorrupt
	}
	ctLen := binary.BigEndian.Uint32(hdr[:4])
	flag := hdr[4]
	if flag != flagMore && flag != flagFinal {
		return ErrCiphertextCorrupt
	}
	if int(ctLen) > r.maxSegment+r.aead.Overhead() {
		return ErrCiphertextCorrupt
	}
	ct := make([]byte, ctLen)
	if _, err := io.ReadFull(r.src, ct); err != nil {
		return ErrCiphertextCorrupt
	}

	var nonce [nonceLen]byte
	copy(nonce[:], r.noncePrefix[:])
	binary.BigEndian.PutUint32(nonce[noncePrefixLen:], r.counter)
	r.counter++

	pt, err := r.aead.Open(nil, nonce[:], ct, []byte{flag})
	if err != nil {
		return ErrCiphertextCorrupt
	}
	r.plain.Reset(pt)
	if flag == flagFinal {
		r.sawFinal = true
	}
	return nil
}

// EncryptedSize returns the exact ciphertext stream size for a plaintext of
// n bytes with the given segment size — used to set Content-Length up front.
func EncryptedSize(n int64, segmentSize int) int64 {
	if segmentSize <= 0 {
		segmentSize = DefaultSegmentSize
	}
	fullSegs := n / int64(segmentSize)
	rem := n % int64(segmentSize)
	overhead := int64(16) // GCM tag
	// Every stream has exactly one final segment (possibly empty). When n is
	// an exact multiple of the segment size, the last full segment becomes
	// the final one; otherwise the remainder (or an empty payload) does.
	segs := fullSegs
	if rem > 0 || fullSegs == 0 {
		segs++
	}
	return int64(magicLen+noncePrefixLen) + segs*int64(segHeaderLen) + segs*overhead + n
}
