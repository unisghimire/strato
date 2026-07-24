package grpc

import (
	"log/slog"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/unisghimire/strato/internal/config"
	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/middleware"
	stratov1 "github.com/unisghimire/strato/proto/gen/strato/v1"
)

// NewServer assembles the gRPC server: OTel stats handler, metrics + the
// full interceptor chain, all three services, and reflection (grpcurl-able
// in every environment; the API is authenticated, not secret).
func NewServer(
	log *slog.Logger,
	tokens domain.TokenIssuer,
	limiter domain.RateLimiter,
	rlCfg config.RateLimit,
	metrics *middleware.Metrics,
	authH *AuthHandler,
	fileH *FileHandler,
	shareH *ShareHandler,
) *grpc.Server {
	srv := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(
			middleware.RequestMeta(),
			middleware.Recovery(log),
			middleware.Logging(log),
			metrics.UnaryInterceptor(),
			middleware.RateLimit(limiter, rlCfg, log),
			middleware.Authentication(tokens),
		),
		// Metadata + JSON payloads are small; large byte transfer goes over
		// the raw HTTP endpoints, so a tight message cap is safe.
		grpc.MaxRecvMsgSize(4<<20),
	)
	stratov1.RegisterAuthServiceServer(srv, authH)
	stratov1.RegisterFileServiceServer(srv, fileH)
	stratov1.RegisterShareServiceServer(srv, shareH)
	reflection.Register(srv)
	return srv
}
