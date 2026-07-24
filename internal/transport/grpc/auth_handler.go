package grpc

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/unisghimire/strato/internal/auth"
	"github.com/unisghimire/strato/internal/usecase"
	stratov1 "github.com/unisghimire/strato/proto/gen/strato/v1"
)

// AuthHandler exposes AuthService over gRPC.
type AuthHandler struct {
	stratov1.UnimplementedAuthServiceServer
	uc *usecase.AuthUseCase
}

// NewAuthHandler constructs an AuthHandler.
func NewAuthHandler(uc *usecase.AuthUseCase) *AuthHandler {
	return &AuthHandler{uc: uc}
}

// Register creates an account.
func (h *AuthHandler) Register(ctx context.Context, req *stratov1.RegisterRequest) (*stratov1.RegisterResponse, error) {
	user, err := h.uc.Register(ctx, req.GetEmail(), req.GetPassword(), req.GetDisplayName())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.RegisterResponse{UserId: user.ID.String()}, nil
}

// Login exchanges credentials for a token pair.
func (h *AuthHandler) Login(ctx context.Context, req *stratov1.LoginRequest) (*stratov1.LoginResponse, error) {
	pair, err := h.uc.Login(ctx, req.GetEmail(), req.GetPassword())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.LoginResponse{
		AccessToken:          pair.AccessToken,
		RefreshToken:         pair.RefreshToken,
		AccessTokenExpiresAt: timestamppb.New(pair.AccessExpiresAt),
	}, nil
}

// RefreshToken rotates a refresh token.
func (h *AuthHandler) RefreshToken(ctx context.Context, req *stratov1.RefreshTokenRequest) (*stratov1.RefreshTokenResponse, error) {
	pair, err := h.uc.Refresh(ctx, req.GetRefreshToken())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.RefreshTokenResponse{
		AccessToken:          pair.AccessToken,
		RefreshToken:         pair.RefreshToken,
		AccessTokenExpiresAt: timestamppb.New(pair.AccessExpiresAt),
	}, nil
}

// Logout revokes the presented refresh token's session family.
func (h *AuthHandler) Logout(ctx context.Context, req *stratov1.LogoutRequest) (*stratov1.LogoutResponse, error) {
	if err := h.uc.Logout(ctx, req.GetRefreshToken()); err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.LogoutResponse{}, nil
}

// GetProfile returns the caller's account and quota.
func (h *AuthHandler) GetProfile(ctx context.Context, _ *stratov1.GetProfileRequest) (*stratov1.GetProfileResponse, error) {
	ident, err := auth.IdentityFromContext(ctx)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	user, quota, err := h.uc.Profile(ctx, ident)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &stratov1.GetProfileResponse{
		UserId:      user.ID.String(),
		Email:       user.Email,
		DisplayName: user.DisplayName,
		Role:        string(user.Role),
		QuotaBytes:  quota.QuotaBytes,
		UsedBytes:   quota.UsedBytes,
		CreatedAt:   timestamppb.New(user.CreatedAt),
	}, nil
}
