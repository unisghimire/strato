// Command worker runs Strato's background garbage collector: expired upload
// sessions, trash purging, orphaned blob deletion, and auth session cleanup.
// It shares the server's repositories but runs as an independently scalable
// process.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/unisghimire/strato/internal/config"
	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/repository/postgres"
	redisrepo "github.com/unisghimire/strato/internal/repository/redis"
	"github.com/unisghimire/strato/internal/service"
	"github.com/unisghimire/strato/internal/storage"
	"github.com/unisghimire/strato/internal/usecase"
	"github.com/unisghimire/strato/pkg/logger"
	"github.com/unisghimire/strato/pkg/workerpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "configs/config.yaml", "path to config file (empty = env only)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	log := logger.New(cfg.Log.Level, cfg.Log.Format)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := postgres.New(ctx, cfg.Postgres)
	if err != nil {
		return err
	}
	defer db.Close()

	redisClient, err := redisrepo.New(ctx, cfg.Redis)
	if err != nil {
		return err
	}
	defer redisClient.Close()

	store, err := storage.NewMinioStore(ctx, cfg.S3)
	if err != nil {
		return err
	}
	kek, err := cfg.Encryption.MasterKeyBytes()
	if err != nil {
		return err
	}

	clock := domain.RealClock{}
	fileRepo := postgres.NewFileRepo(db)
	folderRepo := postgres.NewFolderRepo(db)
	versionRepo := postgres.NewVersionRepo(db)
	blobRepo := postgres.NewBlobRepo(db)
	uploadRepo := postgres.NewUploadRepo(db)
	sessionRepo := postgres.NewSessionRepo(db)
	shareRepo := postgres.NewShareRepo(db)
	quotaRepo := postgres.NewQuotaRepo(db)
	auditRepo := postgres.NewAuditRepo(db)
	auditor := usecase.NewAuditor(auditRepo, log)
	dlock := redisrepo.NewLock(redisClient)

	// The worker reuses the upload use case solely for CleanupSession, so
	// staging-key knowledge stays in one place.
	uploadUC := usecase.NewUploadUseCase(db, uploadRepo, fileRepo, folderRepo, versionRepo, blobRepo,
		shareRepo, quotaRepo, store, dlock, auditor, clock, kek,
		usecase.UploadConfig{
			ChunkSize:   cfg.Upload.ChunkSize,
			MaxFileSize: cfg.Upload.MaxFileSize,
			SessionTTL:  cfg.Upload.SessionTTL,
		})

	pool := workerpool.New(cfg.Worker.PoolSize, cfg.Worker.PoolSize*4)
	gc := service.NewGC(db, fileRepo, versionRepo, blobRepo, uploadRepo, sessionRepo, quotaRepo,
		store, uploadUC, clock, pool, log,
		service.GCConfig{
			TrashRetention: cfg.Worker.TrashRetention,
			OrphanGrace:    cfg.Worker.OrphanGrace,
		})

	log.Info("strato worker starting", "gc_interval", cfg.Worker.GCInterval.String())
	gc.Run(ctx, cfg.Worker.GCInterval)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return pool.Shutdown(shutdownCtx)
}
