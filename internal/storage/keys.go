package storage

import (
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
)

// Object key layout. Blob keys shard by checksum prefix (16²=256 fan-out per
// level) to avoid hot prefixes; S3-compatible stores partition by key prefix.
//
//	blobs/ab/cd/<sha256-hex>          — final encrypted content
//	staging/<session-id>/<index>      — raw uploaded chunks, GC'd on complete/abort

// BlobKey returns the object key for content with the given SHA-256.
func BlobKey(sha256 []byte) string {
	h := hex.EncodeToString(sha256)
	return fmt.Sprintf("blobs/%s/%s/%s", h[:2], h[2:4], h)
}

// ChunkKey returns the staging key for an upload-session chunk.
func ChunkKey(sessionID uuid.UUID, index int) string {
	return fmt.Sprintf("staging/%s/%06d", sessionID, index)
}
