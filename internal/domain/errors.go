// Package domain defines the ports (interfaces) and error taxonomy that the
// use-case layer depends on. Implementations live in outer layers
// (repository, storage, auth); nothing here imports infrastructure.
package domain

import "errors"

// Sentinel domain errors. Use cases return these (optionally wrapped with
// %w and context); the transport layer maps them to gRPC/HTTP status codes.
// Handlers never inspect infrastructure errors directly.
var (
	// ErrNotFound covers missing or inaccessible resources. Authorization
	// failures on resources the caller shouldn't know exist are deliberately
	// mapped to ErrNotFound to prevent IDOR-based existence probing.
	ErrNotFound = errors.New("resource not found")

	// ErrAlreadyExists covers uniqueness violations (email taken, duplicate
	// sibling name).
	ErrAlreadyExists = errors.New("resource already exists")

	// ErrUnauthenticated covers missing/invalid credentials or tokens.
	ErrUnauthenticated = errors.New("unauthenticated")

	// ErrPermissionDenied covers valid identity, insufficient rights.
	ErrPermissionDenied = errors.New("permission denied")

	// ErrInvalidArgument covers validation failures; wrap it with specifics.
	ErrInvalidArgument = errors.New("invalid argument")

	// ErrQuotaExceeded is returned when a write would exceed storage quota.
	ErrQuotaExceeded = errors.New("storage quota exceeded")

	// ErrFileLocked is returned when another user holds the file lock.
	ErrFileLocked = errors.New("file is locked by another user")

	// ErrChecksumMismatch is returned when uploaded content does not match
	// its declared SHA-256.
	ErrChecksumMismatch = errors.New("checksum mismatch")

	// ErrUploadIncomplete is returned by CompleteUpload while chunks are
	// still missing.
	ErrUploadIncomplete = errors.New("upload incomplete: missing chunks")

	// ErrUploadExpired is returned for operations on expired sessions.
	ErrUploadExpired = errors.New("upload session expired")

	// ErrTokenReuse signals refresh-token replay: the session family has
	// been revoked and the user must log in again.
	ErrTokenReuse = errors.New("refresh token reuse detected")

	// ErrRateLimited is returned when the caller exceeds request limits.
	ErrRateLimited = errors.New("rate limit exceeded")

	// ErrPasswordRequired is returned for password-protected public links
	// accessed without (or with a wrong) password.
	ErrPasswordRequired = errors.New("share password required or incorrect")
)
