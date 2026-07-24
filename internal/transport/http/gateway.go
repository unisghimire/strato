package http

import (
	"context"
	"fmt"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	stratov1 "github.com/unisghimire/strato/proto/gen/strato/v1"
)

// NewGateway builds the grpc-gateway mux that translates REST+JSON to the
// in-process gRPC server. The gateway dials loopback rather than invoking
// handlers directly so REST traffic passes the exact same interceptor chain
// (auth, rate limit, metrics) as native gRPC clients.
func NewGateway(ctx context.Context, grpcAddr string) (http.Handler, error) {
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dialing grpc for gateway: %w", err)
	}

	mux := runtime.NewServeMux(
		// Forward auth and proxy headers into gRPC metadata.
		runtime.WithIncomingHeaderMatcher(func(key string) (string, bool) {
			switch key {
			case "Authorization", "X-Forwarded-For", "User-Agent":
				return key, true
			default:
				return runtime.DefaultHeaderMatcher(key)
			}
		}),
	)

	for _, register := range []func(context.Context, *runtime.ServeMux, *grpc.ClientConn) error{
		stratov1.RegisterAuthServiceHandler,
		stratov1.RegisterFileServiceHandler,
		stratov1.RegisterShareServiceHandler,
	} {
		if err := register(ctx, mux, conn); err != nil {
			return nil, fmt.Errorf("registering gateway handler: %w", err)
		}
	}
	return mux, nil
}
