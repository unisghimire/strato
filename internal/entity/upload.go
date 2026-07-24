package entity

import (
	"time"

	"github.com/google/uuid"
)

// UploadStatus is the lifecycle state of an upload session.
type UploadStatus string

// Upload session states.
const (
	UploadPending   UploadStatus = "pending"
	UploadCompleted UploadStatus = "completed"
	UploadAborted   UploadStatus = "aborted"
	UploadExpired   UploadStatus = "expired"
)

// UploadSession is a resumable chunked upload in progress. Chunks land in
// object storage under a staging prefix; CompleteUpload verifies, dedups,
// encrypts, and promotes them into a Blob.
type UploadSession struct {
	ID             uuid.UUID
	UserID         uuid.UUID
	FolderID       *uuid.UUID
	Name           string
	MimeType       string
	SizeBytes      int64
	ChecksumSHA256 []byte
	ChunkSize      int64
	TotalChunks    int
	Status         UploadStatus
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// IsActive reports whether the session still accepts chunks.
func (u *UploadSession) IsActive(now time.Time) bool {
	return u.Status == UploadPending && now.Before(u.ExpiresAt)
}

// ExpectedChunkSize returns the exact size chunk index must have.
func (u *UploadSession) ExpectedChunkSize(index int) int64 {
	if index == u.TotalChunks-1 {
		if rem := u.SizeBytes % u.ChunkSize; rem != 0 {
			return rem
		}
	}
	return u.ChunkSize
}

// Chunk is one received piece of an upload session.
type Chunk struct {
	SessionID      uuid.UUID
	Index          int
	SizeBytes      int64
	ChecksumSHA256 []byte
	StorageKey     string
	UploadedAt     time.Time
}
