// Package grpc implements the gRPC transport: thin handlers that translate
// between protobuf messages and use-case calls. No business logic lives
// here — handlers validate shape, delegate, and map errors.
package grpc

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/pkg/logger"
	"github.com/unisghimire/strato/pkg/pagination"
)

// toStatus maps domain errors to gRPC status codes. Internal error details
// are logged server-side and never leaked to clients.
func toStatus(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, "resource not found")
	case errors.Is(err, domain.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, domain.ErrInvalidArgument), errors.Is(err, pagination.ErrInvalidCursor):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, domain.ErrUnauthenticated), errors.Is(err, domain.ErrTokenReuse):
		return status.Error(codes.Unauthenticated, "authentication required")
	case errors.Is(err, domain.ErrPermissionDenied):
		return status.Error(codes.PermissionDenied, "permission denied")
	case errors.Is(err, domain.ErrQuotaExceeded):
		return status.Error(codes.ResourceExhausted, "storage quota exceeded")
	case errors.Is(err, domain.ErrRateLimited):
		return status.Error(codes.ResourceExhausted, "rate limit exceeded")
	case errors.Is(err, domain.ErrFileLocked),
		errors.Is(err, domain.ErrUploadIncomplete),
		errors.Is(err, domain.ErrUploadExpired),
		errors.Is(err, domain.ErrChecksumMismatch),
		errors.Is(err, domain.ErrPasswordRequired):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	default:
		logger.FromContext(ctx).Error("internal error", "error", err)
		return status.Error(codes.Internal, "internal server error")
	}
}
