package entity

import (
	"time"

	"github.com/google/uuid"
)

// Folder is a named container. ParentID nil means the user's root.
type Folder struct {
	ID        uuid.UUID
	OwnerID   uuid.UUID
	ParentID  *uuid.UUID
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// File is user-visible metadata; bytes live in content-addressed Blobs
// referenced through immutable Versions.
type File struct {
	ID               uuid.UUID
	OwnerID          uuid.UUID
	FolderID         *uuid.UUID
	Name             string
	MimeType         string
	CurrentVersionID *uuid.UUID
	LockedBy         *uuid.UUID
	LockedAt         *time.Time
	IsDeleted        bool
	DeletedAt        *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time

	// Denormalized from the current version for read paths; repositories
	// populate these via join.
	SizeBytes      int64
	ChecksumSHA256 []byte
	VersionNumber  int
}

// IsLockedByOther reports whether userID is blocked from modifying the file.
func (f *File) IsLockedByOther(userID uuid.UUID) bool {
	return f.LockedBy != nil && *f.LockedBy != userID
}

// Version is one immutable entry in a file's history. Restoring an old
// version appends a new Version pointing at the same blob — history is
// never rewritten.
type Version struct {
	ID            uuid.UUID
	FileID        uuid.UUID
	VersionNumber int
	BlobID        uuid.UUID
	SizeBytes     int64
	CreatedBy     uuid.UUID
	CreatedAt     time.Time

	// Denormalized from the blob.
	ChecksumSHA256 []byte
}

// Blob is deduplicated, encrypted content addressed by plaintext SHA-256.
// RefCount counts referencing versions; the GC worker deletes storage
// objects for blobs that reach zero.
type Blob struct {
	ID             uuid.UUID
	ChecksumSHA256 []byte
	SizeBytes      int64
	StorageKey     string
	WrappedDEK     []byte
	RefCount       int
	CreatedAt      time.Time
}
