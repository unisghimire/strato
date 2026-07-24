package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
	"github.com/unisghimire/strato/pkg/workerpool"
)

// GCConfig tunes the garbage collector.
type GCConfig struct {
	// TrashRetention: soft-deleted files older than this are purged.
	TrashRetention time.Duration
	// OrphanGrace: zero-ref blobs younger than this are spared (an upload
	// may still be attaching a version).
	OrphanGrace time.Duration
	// BatchSize bounds work per sweep so a backlog cannot starve a cycle.
	BatchSize int
}

// SessionCleaner is what GC needs from the upload use case.
type SessionCleaner interface {
	CleanupSession(ctx context.Context, sess *entity.UploadSession) error
}

// GC reclaims storage in four independent sweeps:
//
//  1. expired upload sessions → mark expired, delete staging chunks
//  2. trashed files past retention → release blob refs + quota, purge rows
//  3. zero-ref blobs past grace → delete object, delete row
//  4. expired auth sessions → delete rows
//
// Every sweep is idempotent and crash-safe: a worker dying mid-sweep leaves
// items to be picked up next cycle.
type GC struct {
	tx       domain.TxManager
	files    domain.FileRepository
	versions domain.VersionRepository
	blobs    domain.BlobRepository
	uploads  domain.UploadRepository
	sessions domain.SessionRepository
	quotas   domain.QuotaRepository
	store    domain.BlobStore
	cleaner  SessionCleaner
	clock    domain.Clock
	pool     *workerpool.Pool
	log      *slog.Logger
	cfg      GCConfig
}

// NewGC wires the garbage collector.
func NewGC(
	tx domain.TxManager,
	files domain.FileRepository,
	versions domain.VersionRepository,
	blobs domain.BlobRepository,
	uploads domain.UploadRepository,
	sessions domain.SessionRepository,
	quotas domain.QuotaRepository,
	store domain.BlobStore,
	cleaner SessionCleaner,
	clock domain.Clock,
	pool *workerpool.Pool,
	log *slog.Logger,
	cfg GCConfig,
) *GC {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	return &GC{
		tx: tx, files: files, versions: versions, blobs: blobs, uploads: uploads,
		sessions: sessions, quotas: quotas, store: store, cleaner: cleaner,
		clock: clock, pool: pool, log: log, cfg: cfg,
	}
}

// Run executes sweeps on the interval until ctx ends.
func (g *GC) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	g.Sweep(ctx) // immediate first pass
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.Sweep(ctx)
		}
	}
}

// Sweep runs all four collectors once.
func (g *GC) Sweep(ctx context.Context) {
	start := g.clock.Now()
	stats := map[string]int{
		"expired_uploads": g.sweepExpiredUploads(ctx),
		"purged_files":    g.sweepTrash(ctx),
		"orphaned_blobs":  g.sweepOrphanedBlobs(ctx),
	}
	if n, err := g.sessions.DeleteExpired(ctx, g.clock.Now()); err == nil {
		stats["expired_sessions"] = int(n)
	} else {
		g.log.Error("gc: deleting expired sessions", "error", err)
	}
	g.log.Info("gc sweep complete", "duration", time.Since(start).String(),
		"expired_uploads", stats["expired_uploads"], "purged_files", stats["purged_files"],
		"orphaned_blobs", stats["orphaned_blobs"], "expired_sessions", stats["expired_sessions"])
}

func (g *GC) sweepExpiredUploads(ctx context.Context) int {
	expired, err := g.uploads.ListExpired(ctx, g.clock.Now(), g.cfg.BatchSize)
	if err != nil {
		g.log.Error("gc: listing expired uploads", "error", err)
		return 0
	}
	for _, sess := range expired {
		sess := sess
		if err := g.pool.Submit(ctx, func(ctx context.Context) {
			if err := g.cleaner.CleanupSession(ctx, sess); err != nil {
				g.log.Error("gc: cleaning upload session", "session_id", sess.ID, "error", err)
			}
		}); err != nil {
			break
		}
	}
	return len(expired)
}

// sweepTrash permanently deletes files whose trash retention has elapsed,
// releasing blob references and quota transactionally with the row purge.
func (g *GC) sweepTrash(ctx context.Context) int {
	cutoff := g.clock.Now().Add(-g.cfg.TrashRetention)
	candidates, err := g.files.ListDeletedBefore(ctx, cutoff, g.cfg.BatchSize)
	if err != nil {
		g.log.Error("gc: listing trash", "error", err)
		return 0
	}
	purged := 0
	for _, f := range candidates {
		err := g.tx.WithinTx(ctx, func(ctx context.Context) error {
			total, err := g.versions.TotalBytes(ctx, f.ID)
			if err != nil {
				return err
			}
			if err := g.blobs.DecrementRefsForFile(ctx, f.ID); err != nil {
				return err
			}
			if err := g.quotas.AddUsage(ctx, f.OwnerID, -total); err != nil {
				return fmt.Errorf("releasing quota: %w", err)
			}
			return g.files.Purge(ctx, f.ID)
		})
		if err != nil {
			g.log.Error("gc: purging file", "file_id", f.ID, "error", err)
			continue
		}
		purged++
	}
	return purged
}

// sweepOrphanedBlobs deletes storage objects and rows for blobs nothing
// references. Object first, then row: if the object delete fails the row
// stays and we retry next sweep; the reverse order could leak objects
// forever.
func (g *GC) sweepOrphanedBlobs(ctx context.Context) int {
	cutoff := g.clock.Now().Add(-g.cfg.OrphanGrace)
	orphans, err := g.blobs.ListOrphaned(ctx, cutoff, g.cfg.BatchSize)
	if err != nil {
		g.log.Error("gc: listing orphaned blobs", "error", err)
		return 0
	}
	deleted := 0
	for _, b := range orphans {
		if err := g.store.Delete(ctx, b.StorageKey); err != nil {
			g.log.Error("gc: deleting blob object", "blob_id", b.ID, "error", err)
			continue
		}
		// Delete re-checks ref_count = 0, closing the race with a dedup hit
		// that resurrected this blob between listing and now.
		if err := g.blobs.Delete(ctx, b.ID); err != nil && !errors.Is(err, domain.ErrNotFound) {
			g.log.Error("gc: deleting blob row", "blob_id", b.ID, "error", err)
			continue
		}
		deleted++
	}
	return deleted
}
