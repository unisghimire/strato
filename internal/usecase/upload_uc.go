package usecase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
	"github.com/unisghimire/strato/internal/storage"
	"github.com/unisghimire/strato/pkg/crypto"
)

// UploadConfig carries upload policy.
type UploadConfig struct {
	ChunkSize   int64
	MaxFileSize int64
	SessionTTL  time.Duration
}

// UploadUseCase implements the resumable chunked upload pipeline:
//
//	InitUpload    — create session, dedup fast-path check
//	UploadChunk   — stream chunk to staging, record receipt (idempotent)
//	Complete      — verify, assemble, encrypt, dedup, attach version, charge quota
//	Abort         — discard session and staging
type UploadUseCase struct {
	tx      domain.TxManager
	uploads domain.UploadRepository
	files   domain.FileRepository
	folders domain.FolderRepository
	verRepo domain.VersionRepository
	blobs   domain.BlobRepository
	shares  domain.ShareRepository
	quotas  domain.QuotaRepository
	store   domain.BlobStore
	dlock   domain.DistributedLock
	auditor *Auditor
	clock   domain.Clock
	kek     []byte
	cfg     UploadConfig
}

// NewUploadUseCase wires the upload use case.
func NewUploadUseCase(
	tx domain.TxManager,
	uploads domain.UploadRepository,
	files domain.FileRepository,
	folders domain.FolderRepository,
	verRepo domain.VersionRepository,
	blobs domain.BlobRepository,
	shares domain.ShareRepository,
	quotas domain.QuotaRepository,
	store domain.BlobStore,
	dlock domain.DistributedLock,
	auditor *Auditor,
	clock domain.Clock,
	kek []byte,
	cfg UploadConfig,
) *UploadUseCase {
	return &UploadUseCase{
		tx: tx, uploads: uploads, files: files, folders: folders, verRepo: verRepo,
		blobs: blobs, shares: shares, quotas: quotas, store: store, dlock: dlock,
		auditor: auditor, clock: clock, kek: kek, cfg: cfg,
	}
}

// InitResult is InitUpload's outcome.
type InitResult struct {
	Session       *entity.UploadSession
	AlreadyExists bool // content dedup hit: client may Complete immediately
}

// InitUpload validates the target and opens a resumable session.
func (u *UploadUseCase) InitUpload(ctx context.Context, ident *domain.Identity, name, folderID, mimeType string, sizeBytes int64, checksumHex string) (*InitResult, error) {
	name, err := validateName(name)
	if err != nil {
		return nil, err
	}
	if sizeBytes <= 0 || sizeBytes > u.cfg.MaxFileSize {
		return nil, fmt.Errorf("%w: size must be in (0, %d] bytes", domain.ErrInvalidArgument, u.cfg.MaxFileSize)
	}
	checksum, err := hex.DecodeString(checksumHex)
	if err != nil || len(checksum) != sha256.Size {
		return nil, fmt.Errorf("%w: checksum_sha256 must be 64 hex characters", domain.ErrInvalidArgument)
	}
	folder, err := parseOptionalID(folderID)
	if err != nil {
		return nil, err
	}
	if folder != nil {
		if f, ferr := u.folders.GetByID(ctx, *folder); ferr != nil || f.OwnerID != ident.UserID {
			return nil, domain.ErrNotFound
		}
	}

	// Uploading a new version of a file someone else has locked fails now,
	// not after gigabytes of transfer.
	if existing, err := u.files.GetByName(ctx, ident.UserID, folder, name); err == nil {
		if existing.IsLockedByOther(ident.UserID) {
			return nil, domain.ErrFileLocked
		}
	}

	// Advisory quota gate: fail fast before bytes move. The authoritative,
	// race-free check happens transactionally at Complete.
	quota, err := u.quotas.Get(ctx, ident.UserID)
	if err != nil {
		return nil, err
	}
	if !quota.CanStore(sizeBytes) {
		return nil, domain.ErrQuotaExceeded
	}

	alreadyExists := false
	if _, err := u.blobs.GetByChecksum(ctx, checksum); err == nil {
		alreadyExists = true // dedup fast path: no bytes needed
	}

	totalChunks := int((sizeBytes + u.cfg.ChunkSize - 1) / u.cfg.ChunkSize)
	sess := &entity.UploadSession{
		ID:             uuid.New(),
		UserID:         ident.UserID,
		FolderID:       folder,
		Name:           name,
		MimeType:       mimeType,
		SizeBytes:      sizeBytes,
		ChecksumSHA256: checksum,
		ChunkSize:      u.cfg.ChunkSize,
		TotalChunks:    totalChunks,
		Status:         entity.UploadPending,
		ExpiresAt:      u.clock.Now().Add(u.cfg.SessionTTL),
	}
	if err := u.uploads.CreateSession(ctx, sess); err != nil {
		return nil, err
	}
	return &InitResult{Session: sess, AlreadyExists: alreadyExists}, nil
}

// UploadChunk streams one chunk into staging storage while hashing it, then
// records the receipt. Retransmission of a chunk overwrites idempotently.
func (u *UploadUseCase) UploadChunk(ctx context.Context, ident *domain.Identity, sessionID uuid.UUID, index int, body io.Reader) (*entity.Chunk, error) {
	sess, err := u.activeSession(ctx, ident, sessionID)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= sess.TotalChunks {
		return nil, fmt.Errorf("%w: chunk index out of range [0,%d)", domain.ErrInvalidArgument, sess.TotalChunks)
	}
	expected := sess.ExpectedChunkSize(index)

	hasher := sha256.New()
	key := storage.ChunkKey(sess.ID, index)
	// Put reads exactly `expected` bytes; a short body fails inside Put, so
	// undersized chunks never produce a receipt.
	if err := u.store.Put(ctx, key, io.TeeReader(io.LimitReader(body, expected), hasher), expected, "application/octet-stream"); err != nil {
		// The infra error is context only — deliberately %v, not %w, so it
		// never joins the domain error chain callers match on.
		return nil, fmt.Errorf("%w: chunk %d rejected (wrong size or transfer error): %v", domain.ErrInvalidArgument, index, err) //nolint:errorlint
	}
	chunk := &entity.Chunk{
		SessionID:      sess.ID,
		Index:          index,
		SizeBytes:      expected,
		ChecksumSHA256: hasher.Sum(nil),
		StorageKey:     key,
	}
	if err := u.uploads.SaveChunk(ctx, chunk); err != nil {
		return nil, err
	}
	return chunk, nil
}

// Status reports which chunks have been received, enabling client resume.
func (u *UploadUseCase) Status(ctx context.Context, ident *domain.Identity, sessionID uuid.UUID) (*entity.UploadSession, []int, error) {
	sess, err := u.ownedSession(ctx, ident, sessionID)
	if err != nil {
		return nil, nil, err
	}
	chunks, err := u.uploads.ListChunks(ctx, sess.ID)
	if err != nil {
		return nil, nil, err
	}
	received := make([]int, 0, len(chunks))
	for _, c := range chunks {
		received = append(received, c.Index)
	}
	return sess, received, nil
}

// Complete finalizes the upload: verify all chunks arrived, assemble and
// hash the plaintext while encrypting it into the blob store, verify the
// declared checksum, then transactionally attach a new file version and
// charge quota. A distributed lock serializes completion across replicas.
func (u *UploadUseCase) Complete(ctx context.Context, ident *domain.Identity, sessionID uuid.UUID) (*entity.File, error) {
	sess, err := u.activeSession(ctx, ident, sessionID)
	if err != nil {
		return nil, err
	}

	release, ok, err := u.dlock.TryLock(ctx, "upload-complete:"+sess.ID.String(), 10*time.Minute)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: completion already in progress", domain.ErrAlreadyExists)
	}
	defer release()

	blob, err := u.blobs.GetByChecksum(ctx, sess.ChecksumSHA256)
	if err != nil {
		if !errIs(err, domain.ErrNotFound) {
			return nil, err
		}
		// No dedup hit: assemble staging chunks into an encrypted blob.
		blob, err = u.assembleBlob(ctx, sess)
		if err != nil {
			return nil, err
		}
	}

	file, err := u.attachVersion(ctx, sess, blob)
	if err != nil {
		return nil, err
	}
	if err := u.uploads.UpdateStatus(ctx, sess.ID, entity.UploadCompleted); err != nil {
		return nil, err
	}
	u.cleanupStaging(ctx, sess)
	u.auditor.Record(ctx, &ident.UserID, entity.AuditFileUploaded, "file", file.ID.String(),
		map[string]any{"size_bytes": sess.SizeBytes, "session_id": sess.ID.String()})
	return u.files.GetByID(ctx, file.ID)
}

// Abort cancels a pending session and discards its staging chunks.
func (u *UploadUseCase) Abort(ctx context.Context, ident *domain.Identity, sessionID uuid.UUID) error {
	sess, err := u.ownedSession(ctx, ident, sessionID)
	if err != nil {
		return err
	}
	if sess.Status != entity.UploadPending {
		return nil // terminal states are idempotent to abort
	}
	if err := u.uploads.UpdateStatus(ctx, sess.ID, entity.UploadAborted); err != nil {
		return err
	}
	u.cleanupStaging(ctx, sess)
	return nil
}

// CleanupSession is invoked by the GC worker for expired sessions.
func (u *UploadUseCase) CleanupSession(ctx context.Context, sess *entity.UploadSession) error {
	if err := u.uploads.UpdateStatus(ctx, sess.ID, entity.UploadExpired); err != nil {
		return err
	}
	u.cleanupStaging(ctx, sess)
	return nil
}

// assembleBlob streams staged chunks in order through a SHA-256 hasher and
// the AES-GCM stream encryptor directly into the blob store — constant
// memory regardless of file size. If the resulting hash does not match the
// declared checksum, the object is deleted and the session stays pending so
// the client can re-send corrupted chunks.
func (u *UploadUseCase) assembleBlob(ctx context.Context, sess *entity.UploadSession) (*entity.Blob, error) {
	chunks, err := u.uploads.ListChunks(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	if len(chunks) != sess.TotalChunks {
		return nil, fmt.Errorf("%w: %d of %d chunks received", domain.ErrUploadIncomplete, len(chunks), sess.TotalChunks)
	}
	for i, c := range chunks {
		if c.Index != i {
			return nil, fmt.Errorf("%w: chunk %d missing", domain.ErrUploadIncomplete, i)
		}
	}

	dek, err := crypto.NewDEK()
	if err != nil {
		return nil, err
	}
	wrappedDEK, err := crypto.WrapKey(u.kek, dek)
	if err != nil {
		return nil, err
	}

	blobKey := storage.BlobKey(sess.ChecksumSHA256)
	hasher := sha256.New()
	pr, pw := io.Pipe()

	// Producer: read chunks sequentially, tee plaintext into the hasher,
	// encrypt into the pipe. Any failure propagates through CloseWithError
	// so the consumer's Put fails fast.
	go func() {
		enc, err := crypto.NewStreamWriter(pw, dek, crypto.DefaultSegmentSize)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		sink := io.MultiWriter(hasher, enc)
		for _, c := range chunks {
			if err := u.copyChunk(ctx, c.StorageKey, sink); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
		if err := enc.Close(); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.Close()
	}()

	encSize := crypto.EncryptedSize(sess.SizeBytes, crypto.DefaultSegmentSize)
	if err := u.store.Put(ctx, blobKey, pr, encSize, "application/octet-stream"); err != nil {
		_ = pr.CloseWithError(err) // unblock producer
		return nil, fmt.Errorf("storing blob: %w", err)
	}

	if !bytes.Equal(hasher.Sum(nil), sess.ChecksumSHA256) {
		_ = u.store.Delete(ctx, blobKey)
		return nil, domain.ErrChecksumMismatch
	}

	blob := &entity.Blob{
		ID:             uuid.New(),
		ChecksumSHA256: sess.ChecksumSHA256,
		SizeBytes:      sess.SizeBytes,
		StorageKey:     blobKey,
		WrappedDEK:     wrappedDEK,
	}
	if err := u.blobs.Create(ctx, blob); err != nil {
		if errIs(err, domain.ErrAlreadyExists) {
			// Concurrent upload of identical content won the race; adopt
			// theirs. Both wrote the same deterministic key, so nothing to
			// delete.
			return u.blobs.GetByChecksum(ctx, sess.ChecksumSHA256)
		}
		return nil, err
	}
	return blob, nil
}

func (u *UploadUseCase) copyChunk(ctx context.Context, key string, dst io.Writer) error {
	rc, err := u.store.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("reading chunk %s: %w", key, err)
	}
	defer rc.Close()
	if _, err := io.Copy(dst, rc); err != nil {
		return fmt.Errorf("copying chunk %s: %w", key, err)
	}
	return nil
}

// attachVersion creates (or reuses) the file row and appends the new version
// atomically with the blob refcount and quota charge. Any failure — quota
// exceeded, lock contention, unique-name race — rolls back everything.
func (u *UploadUseCase) attachVersion(ctx context.Context, sess *entity.UploadSession, blob *entity.Blob) (*entity.File, error) {
	var file *entity.File
	err := u.tx.WithinTx(ctx, func(ctx context.Context) error {
		existing, err := u.files.GetByName(ctx, sess.UserID, sess.FolderID, sess.Name)
		switch {
		case err == nil:
			if existing.IsLockedByOther(sess.UserID) {
				return domain.ErrFileLocked
			}
			file = existing
		case errIs(err, domain.ErrNotFound):
			file = &entity.File{
				ID:       uuid.New(),
				OwnerID:  sess.UserID,
				FolderID: sess.FolderID,
				Name:     sess.Name,
				MimeType: sess.MimeType,
			}
			if err := u.files.Create(ctx, file); err != nil {
				return err
			}
		default:
			return err
		}

		version := &entity.Version{
			ID:        uuid.New(),
			FileID:    file.ID,
			BlobID:    blob.ID,
			SizeBytes: sess.SizeBytes,
			CreatedBy: sess.UserID,
		}
		if err := u.verRepo.Create(ctx, version); err != nil {
			return err
		}
		if err := u.blobs.IncrementRef(ctx, blob.ID); err != nil {
			return err
		}
		if err := u.files.SetCurrentVersion(ctx, file.ID, version.ID); err != nil {
			return err
		}
		// Authoritative quota enforcement: single-statement check-and-add.
		return u.quotas.AddUsage(ctx, sess.UserID, sess.SizeBytes)
	})
	if err != nil {
		return nil, err
	}
	return file, nil
}

// cleanupStaging deletes staged chunk objects best-effort; anything missed
// is swept by the GC worker via the session's chunk receipts.
func (u *UploadUseCase) cleanupStaging(ctx context.Context, sess *entity.UploadSession) {
	chunks, err := u.uploads.ListChunks(ctx, sess.ID)
	if err != nil {
		return
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for _, c := range chunks {
		_ = u.store.Delete(dctx, c.StorageKey)
	}
}

func (u *UploadUseCase) ownedSession(ctx context.Context, ident *domain.Identity, sessionID uuid.UUID) (*entity.UploadSession, error) {
	sess, err := u.uploads.GetSession(ctx, sessionID)
	if err != nil || sess.UserID != ident.UserID {
		return nil, domain.ErrNotFound // hide other users' sessions (IDOR)
	}
	return sess, nil
}

func (u *UploadUseCase) activeSession(ctx context.Context, ident *domain.Identity, sessionID uuid.UUID) (*entity.UploadSession, error) {
	sess, err := u.ownedSession(ctx, ident, sessionID)
	if err != nil {
		return nil, err
	}
	if sess.Status != entity.UploadPending {
		return nil, fmt.Errorf("%w: session is %s", domain.ErrInvalidArgument, sess.Status)
	}
	if !sess.IsActive(u.clock.Now()) {
		return nil, domain.ErrUploadExpired
	}
	return sess, nil
}
