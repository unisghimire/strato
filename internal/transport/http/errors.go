// Package http implements the REST side of the transport: the grpc-gateway
// for JSON APIs plus raw streaming handlers for byte transfer (chunk upload,
// download, public links) where a JSON gateway would be the wrong tool.
package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/pkg/logger"
)

// writeError maps domain errors to HTTP status codes with a small JSON body,
// mirroring grpc-gateway's error shape. Internal details are logged, not
// returned.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	code := http.StatusInternalServerError
	msg := "internal server error"

	switch {
	case errors.Is(err, domain.ErrNotFound):
		code, msg = http.StatusNotFound, "resource not found"
	case errors.Is(err, domain.ErrInvalidArgument):
		code, msg = http.StatusBadRequest, err.Error()
	case errors.Is(err, domain.ErrAlreadyExists):
		code, msg = http.StatusConflict, err.Error()
	case errors.Is(err, domain.ErrUnauthenticated):
		code, msg = http.StatusUnauthorized, "authentication required"
	case errors.Is(err, domain.ErrPermissionDenied):
		code, msg = http.StatusForbidden, "permission denied"
	case errors.Is(err, domain.ErrQuotaExceeded):
		code, msg = http.StatusInsufficientStorage, "storage quota exceeded"
	case errors.Is(err, domain.ErrPasswordRequired):
		code, msg = http.StatusUnauthorized, "share password required or incorrect"
	case errors.Is(err, domain.ErrFileLocked),
		errors.Is(err, domain.ErrUploadIncomplete),
		errors.Is(err, domain.ErrUploadExpired),
		errors.Is(err, domain.ErrChecksumMismatch):
		code, msg = http.StatusPreconditionFailed, err.Error()
	default:
		logger.FromContext(r.Context()).Error("internal error", "error", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
