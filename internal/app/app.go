// Package app performs dependency injection and process lifecycle for the
// API server. cmd/server stays a thin main; everything composable lives
// here where it can be constructed in tests too.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	"github.com/unisghimire/strato/internal/auth"
	"github.com/unisghimire/strato/internal/config"
	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/middleware"
	"github.com/unisghimire/strato/internal/repository/postgres"
	redisrepo "github.com/unisghimire/strato/internal/repository/redis"
	"github.com/unisghimire/strato/internal/service"
	"github.com/unisghimire/strato/internal/storage"
	grpctransport "github.com/unisghimire/strato/internal/transport/grpc"
	httptransport "github.com/unisghimire/strato/internal/transport/http"
	"github.com/unisghimire/strato/internal/usecase"
)

// App is the fully wired server process.
type App struct {
	cfg        *config.Config
	log        *slog.Logger
	db         *postgres.DB
	redis      *goredis.Client
	grpcServer *grpc.Server
	httpServer *http.Server
	metricsSrv *http.Server
	shutdownFn []func(context.Context) error
}

// New wires every dependency. Construction is fail-fast: any unreachable
// backend or invalid config aborts startup rather than limping.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	a := &App{cfg: cfg, log: log}

	// --- Infrastructure ---
	tracerShutdown, err := setupTracing(ctx, cfg.Telemetry)
	if err != nil {
		return nil, err
	}
	a.shutdownFn = append(a.shutdownFn, tracerShutdown)

	a.db, err = postgres.New(ctx, cfg.Postgres)
	if err != nil {
		return nil, err
	}
	a.redis, err = redisrepo.New(ctx, cfg.Redis)
	if err != nil {
		return nil, err
	}
	store, err := storage.NewMinioStore(ctx, cfg.S3)
	if err != nil {
		return nil, err
	}
	kek, err := cfg.Encryption.MasterKeyBytes()
	if err != nil {
		return nil, err
	}

	// --- Ports ---
	clock := domain.RealClock{}
	userRepo := postgres.NewUserRepo(a.db)
	sessionRepo := postgres.NewSessionRepo(a.db)
	folderRepo := postgres.NewFolderRepo(a.db)
	fileRepo := postgres.NewFileRepo(a.db)
	versionRepo := postgres.NewVersionRepo(a.db)
	blobRepo := postgres.NewBlobRepo(a.db)
	uploadRepo := postgres.NewUploadRepo(a.db)
	shareRepo := postgres.NewShareRepo(a.db)
	quotaRepo := postgres.NewQuotaRepo(a.db)
	auditRepo := postgres.NewAuditRepo(a.db)

	limiter := redisrepo.NewRateLimiter(a.redis)
	dlock := redisrepo.NewLock(a.redis)
	tokens := auth.NewJWTManager(cfg.Auth, clock)
	signer := service.NewURLSigner([]byte(cfg.Auth.JWTSecret), clock)
	auditor := usecase.NewAuditor(auditRepo, log)

	// --- Use cases ---
	authUC, err := usecase.NewAuthUseCase(a.db, userRepo, sessionRepo, quotaRepo, tokens, auditor, clock,
		usecase.AuthConfig{
			RefreshTokenTTL:   cfg.Auth.RefreshTokenTTL,
			DefaultQuotaBytes: cfg.Quota.DefaultBytes,
		})
	if err != nil {
		return nil, err
	}
	fileUC := usecase.NewFileUseCase(a.db, fileRepo, folderRepo, versionRepo, blobRepo, shareRepo,
		quotaRepo, store, signer, auditor, clock, kek,
		usecase.FileConfig{SignedURLTTL: cfg.S3.SignedURLTTL})
	uploadUC := usecase.NewUploadUseCase(a.db, uploadRepo, fileRepo, folderRepo, versionRepo, blobRepo,
		shareRepo, quotaRepo, store, dlock, auditor, clock, kek,
		usecase.UploadConfig{
			ChunkSize:   cfg.Upload.ChunkSize,
			MaxFileSize: cfg.Upload.MaxFileSize,
			SessionTTL:  cfg.Upload.SessionTTL,
		})
	shareUC := usecase.NewShareUseCase(shareRepo, fileRepo, userRepo, auditor, clock)

	// --- Transport ---
	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	metrics := middleware.NewMetrics(registry)

	a.grpcServer = grpctransport.NewServer(log, tokens, limiter, cfg.RateLimit, metrics,
		grpctransport.NewAuthHandler(authUC),
		grpctransport.NewFileHandler(fileUC, uploadUC),
		grpctransport.NewShareHandler(shareUC, ""),
	)

	gateway, err := httptransport.NewGateway(ctx, gatewayTarget(cfg.Server.GRPCAddr))
	if err != nil {
		return nil, err
	}
	router := httptransport.NewRouter(httptransport.RouterDeps{
		Log:      log,
		Tokens:   tokens,
		Limiter:  limiter,
		RateCfg:  cfg.RateLimit,
		Gateway:  gateway,
		Upload:   httptransport.NewUploadHandler(uploadUC, metrics, cfg.Upload.ChunkSize),
		Download: httptransport.NewDownloadHandler(fileUC, metrics),
		Public:   httptransport.NewPublicHandler(shareUC, fileUC, metrics),
		ReadyChecks: map[string]func(context.Context) error{
			"postgres": a.db.Ping,
			"redis":    func(ctx context.Context) error { return a.redis.Ping(ctx).Err() },
		},
	})

	a.httpServer = &http.Server{
		Addr:              cfg.Server.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		// No blanket ReadTimeout/WriteTimeout: chunk uploads and large
		// downloads are legitimately long-lived. Idle and header timeouts
		// still bound slowloris-style abuse.
		IdleTimeout: 2 * time.Minute,
	}
	a.metricsSrv = &http.Server{
		Addr:              cfg.Server.MetricsAddr,
		Handler:           promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return a, nil
}

// Run serves until ctx is canceled, then drains gracefully.
func (a *App) Run(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)

	grpcLis, err := net.Listen("tcp", a.cfg.Server.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", a.cfg.Server.GRPCAddr, err)
	}

	g.Go(func() error {
		a.log.Info("grpc server listening", "addr", a.cfg.Server.GRPCAddr)
		return a.grpcServer.Serve(grpcLis)
	})
	g.Go(func() error {
		a.log.Info("http server listening", "addr", a.cfg.Server.HTTPAddr)
		if err := listenAndServe(a.httpServer, a.cfg.Server.TLS); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		a.log.Info("metrics server listening", "addr", a.cfg.Server.MetricsAddr)
		if err := a.metricsSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		a.log.Info("shutting down", "timeout", a.cfg.Server.ShutdownTimeout.String())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.Server.ShutdownTimeout)
		defer cancel()

		done := make(chan struct{})
		go func() {
			a.grpcServer.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
		case <-shutdownCtx.Done():
			a.grpcServer.Stop()
		}
		_ = a.httpServer.Shutdown(shutdownCtx)
		_ = a.metricsSrv.Shutdown(shutdownCtx)
		for _, fn := range a.shutdownFn {
			_ = fn(shutdownCtx)
		}
		a.db.Close()
		_ = a.redis.Close()
		return nil
	})
	return g.Wait()
}

func listenAndServe(srv *http.Server, tls config.TLS) error {
	if tls.Enabled {
		return srv.ListenAndServeTLS(tls.CertFile, tls.KeyFile)
	}
	return srv.ListenAndServe()
}

// gatewayTarget converts a listen address (":9090") into a dialable loopback
// target.
func gatewayTarget(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return listenAddr
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
