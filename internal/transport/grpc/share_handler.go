package grpc

import (
	"context"

	"github.com/unisghimire/strato/internal/auth"
	"github.com/unisghimire/strato/internal/usecase"
	stratov1 "github.com/unisghimire/strato/proto/gen/strato/v1"
)

// ShareHandler exposes ShareService over gRPC.
type ShareHandler struct {
	stratov1.UnimplementedShareServiceServer
	uc *usecase.ShareUseCase
	// publicBaseURL prefixes public-link paths in responses, e.g.
	// "https://files.example.com".
	publicBaseURL string
}

// NewShareHandler constructs a ShareHandler.
func NewShareHandler(uc *usecase.ShareUseCase, publicBaseURL string) *ShareHandler {
	return &ShareHandler{uc: uc, publicBaseURL: publicBaseURL}
}

// CreateShare grants a named user access to a file.
func (h *ShareHandler) CreateShare(ctx context.Context, req *stratov1.CreateShareRequest) (*stratov1.CreateShareResponse, error) {
	ident, err := auth.IdentityFromContext(ctx)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	fileID, err := parseUUID(req.GetFileId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	share, err := h.uc.CreateShare(ctx, ident, fileID, req.GetGranteeEmail(),
		permissionFromProto(req.GetPermission()), optionalTime(req.GetExpiresAt()))
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.CreateShareResponse{
		Share: shareToProto(share, req.GetGranteeEmail(), ""),
	}, nil
}

// CreatePublicLink mints a tokenized link. The token appears exactly once,
// in this response.
func (h *ShareHandler) CreatePublicLink(ctx context.Context, req *stratov1.CreatePublicLinkRequest) (*stratov1.CreatePublicLinkResponse, error) {
	ident, err := auth.IdentityFromContext(ctx)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	fileID, err := parseUUID(req.GetFileId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	share, token, err := h.uc.CreatePublicLink(ctx, ident, fileID,
		permissionFromProto(req.GetPermission()), optionalTime(req.GetExpiresAt()), req.GetPassword())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.CreatePublicLinkResponse{
		Share: shareToProto(share, "", h.publicBaseURL+"/public/"+token),
	}, nil
}

// ListShares pages shares created by the caller.
func (h *ShareHandler) ListShares(ctx context.Context, req *stratov1.ListSharesRequest) (*stratov1.ListSharesResponse, error) {
	ident, err := auth.IdentityFromContext(ctx)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	fileID, err := optionalUUID(req.GetFileId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	cur, limit, err := pageFromProto(req.GetPage())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	shares, err := h.uc.List(ctx, ident, fileID, cur, limit)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	out := make([]*stratov1.Share, len(shares))
	for i, s := range shares {
		// Token digests are one-way; public URLs cannot be reconstructed
		// after creation, by design.
		out[i] = shareToProto(s, "", "")
	}
	resp := &stratov1.ListSharesResponse{Shares: out, Page: &stratov1.PageResponse{}}
	if len(shares) == limit {
		last := shares[len(shares)-1]
		resp.Page = nextPageToken(len(shares), limit, last.CreatedAt, last.ID.String())
	}
	return resp, nil
}

// RevokeShare deactivates a share.
func (h *ShareHandler) RevokeShare(ctx context.Context, req *stratov1.RevokeShareRequest) (*stratov1.RevokeShareResponse, error) {
	ident, err := auth.IdentityFromContext(ctx)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	shareID, err := parseUUID(req.GetShareId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	if err := h.uc.Revoke(ctx, ident, shareID); err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.RevokeShareResponse{}, nil
}
