package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/auth"
	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
	"github.com/unisghimire/strato/pkg/crypto"
)

// AuthConfig carries the policy knobs the auth use case needs.
type AuthConfig struct {
	RefreshTokenTTL   time.Duration
	DefaultQuotaBytes int64
}

// AuthUseCase implements registration, login, and refresh-token rotation.
type AuthUseCase struct {
	tx       domain.TxManager
	users    domain.UserRepository
	sessions domain.SessionRepository
	quotas   domain.QuotaRepository
	tokens   domain.TokenIssuer
	auditor  *Auditor
	clock    domain.Clock
	cfg      AuthConfig

	// dummyHash burns a real Argon2id verification when the email does not
	// exist, keeping login timing independent of account existence.
	dummyHash string
}

// NewAuthUseCase wires the auth use case.
func NewAuthUseCase(
	tx domain.TxManager,
	users domain.UserRepository,
	sessions domain.SessionRepository,
	quotas domain.QuotaRepository,
	tokens domain.TokenIssuer,
	auditor *Auditor,
	clock domain.Clock,
	cfg AuthConfig,
) (*AuthUseCase, error) {
	dummy, err := crypto.HashPassword(uuid.NewString(), crypto.DefaultArgon2Params)
	if err != nil {
		return nil, fmt.Errorf("precomputing dummy hash: %w", err)
	}
	return &AuthUseCase{
		tx: tx, users: users, sessions: sessions, quotas: quotas,
		tokens: tokens, auditor: auditor, clock: clock, cfg: cfg, dummyHash: dummy,
	}, nil
}

// TokenPair is the credential set returned by Login and Refresh.
type TokenPair struct {
	AccessToken     string
	RefreshToken    string
	AccessExpiresAt time.Time
}

// Register creates an account with its quota row in one transaction.
func (u *AuthUseCase) Register(ctx context.Context, email, password, displayName string) (*entity.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if err := validateEmail(email); err != nil {
		return nil, err
	}
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	if len(displayName) > 100 {
		return nil, fmt.Errorf("%w: display name too long", domain.ErrInvalidArgument)
	}

	hash, err := crypto.HashPassword(password, crypto.DefaultArgon2Params)
	if err != nil {
		return nil, fmt.Errorf("hashing password: %w", err)
	}
	user := &entity.User{
		ID:           uuid.New(),
		Email:        email,
		PasswordHash: hash,
		DisplayName:  strings.TrimSpace(displayName),
		Role:         entity.RoleUser,
	}
	err = u.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := u.users.Create(ctx, user); err != nil {
			return err
		}
		return u.quotas.Create(ctx, &entity.Quota{
			UserID:     user.ID,
			QuotaBytes: u.cfg.DefaultQuotaBytes,
		})
	})
	if err != nil {
		if errIs(err, domain.ErrAlreadyExists) {
			return nil, fmt.Errorf("%w: email already registered", domain.ErrAlreadyExists)
		}
		return nil, err
	}
	u.auditor.Record(ctx, &user.ID, entity.AuditUserRegistered, "user", user.ID.String(), nil)
	return user, nil
}

// Login verifies credentials and mints a token pair with a new session
// family. Timing is uniform across "no such user" and "wrong password".
func (u *AuthUseCase) Login(ctx context.Context, email, password string) (*TokenPair, error) {
	email = strings.ToLower(strings.TrimSpace(email))

	user, err := u.users.GetByEmail(ctx, email)
	if err != nil {
		// Burn an equivalent verification so response time does not reveal
		// whether the account exists.
		_, _ = crypto.VerifyPassword(password, u.dummyHash)
		return nil, domain.ErrUnauthenticated
	}
	ok, err := crypto.VerifyPassword(password, user.PasswordHash)
	if err != nil || !ok {
		u.auditor.Record(ctx, &user.ID, entity.AuditUserLoginFailed, "user", user.ID.String(), nil)
		return nil, domain.ErrUnauthenticated
	}

	// Transparently upgrade hashes when the Argon2id policy has been raised.
	if crypto.NeedsRehash(user.PasswordHash, crypto.DefaultArgon2Params) {
		if newHash, hashErr := crypto.HashPassword(password, crypto.DefaultArgon2Params); hashErr == nil {
			_ = u.users.UpdatePasswordHash(ctx, user.ID, newHash)
		}
	}

	pair, err := u.mintPair(ctx, user, uuid.New(), nil)
	if err != nil {
		return nil, err
	}
	u.auditor.Record(ctx, &user.ID, entity.AuditUserLogin, "user", user.ID.String(), nil)
	return pair, nil
}

// Refresh rotates a refresh token. Presenting an already-rotated (revoked)
// token is treated as theft: the entire session family is revoked and the
// legitimate user must re-authenticate.
func (u *AuthUseCase) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	sess, err := u.sessions.GetByTokenHash(ctx, crypto.HashToken(refreshToken))
	if err != nil {
		return nil, domain.ErrUnauthenticated
	}
	now := u.clock.Now()
	if sess.RevokedAt != nil {
		// Only a rotated-out token (one with a successor) signals replay
		// theft; tokens revoked by logout or a prior theft response are
		// simply dead credentials.
		if sess.ReplacedBy != nil {
			_ = u.sessions.RevokeFamily(ctx, sess.FamilyID)
			u.auditor.Record(ctx, &sess.UserID, entity.AuditTokenReuse, "session", sess.ID.String(),
				map[string]any{"family_id": sess.FamilyID.String()})
			return nil, domain.ErrTokenReuse
		}
		return nil, domain.ErrUnauthenticated
	}
	if !sess.IsActive(now) {
		return nil, domain.ErrUnauthenticated
	}
	user, err := u.users.GetByID(ctx, sess.UserID)
	if err != nil {
		return nil, domain.ErrUnauthenticated
	}
	return u.mintPair(ctx, user, sess.FamilyID, &sess.ID)
}

// Logout revokes the presented refresh token's whole session family.
// Idempotent: logging out twice succeeds.
func (u *AuthUseCase) Logout(ctx context.Context, refreshToken string) error {
	sess, err := u.sessions.GetByTokenHash(ctx, crypto.HashToken(refreshToken))
	if err != nil {
		return nil // unknown token: nothing to revoke
	}
	if err := u.sessions.RevokeFamily(ctx, sess.FamilyID); err != nil {
		return err
	}
	u.auditor.Record(ctx, &sess.UserID, entity.AuditUserLogout, "session", sess.ID.String(), nil)
	return nil
}

// Profile returns the caller's account and quota.
func (u *AuthUseCase) Profile(ctx context.Context, ident *domain.Identity) (*entity.User, *entity.Quota, error) {
	user, err := u.users.GetByID(ctx, ident.UserID)
	if err != nil {
		return nil, nil, err
	}
	quota, err := u.quotas.Get(ctx, ident.UserID)
	if err != nil {
		return nil, nil, err
	}
	return user, quota, nil
}

// mintPair creates a session row (rotating out prev, if any) and issues the
// access token. Rotation is transactional: the old token is dead if and only
// if the new one exists.
func (u *AuthUseCase) mintPair(ctx context.Context, user *entity.User, familyID uuid.UUID, prevSessionID *uuid.UUID) (*TokenPair, error) {
	refreshToken, err := crypto.RandomToken(32)
	if err != nil {
		return nil, err
	}
	meta := auth.RequestMetaFromContext(ctx)
	sess := &entity.Session{
		ID:        uuid.New(),
		UserID:    user.ID,
		FamilyID:  familyID,
		TokenHash: crypto.HashToken(refreshToken),
		UserAgent: meta.UserAgent,
		IP:        meta.IP,
		ExpiresAt: u.clock.Now().Add(u.cfg.RefreshTokenTTL),
	}
	err = u.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := u.sessions.Create(ctx, sess); err != nil {
			return err
		}
		if prevSessionID != nil {
			return u.sessions.Revoke(ctx, *prevSessionID, &sess.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	accessToken, expiresAt, err := u.tokens.IssueAccessToken(user)
	if err != nil {
		return nil, err
	}
	return &TokenPair{
		AccessToken:     accessToken,
		RefreshToken:    refreshToken,
		AccessExpiresAt: expiresAt,
	}, nil
}

func validateEmail(email string) error {
	if len(email) < 3 || len(email) > 254 {
		return fmt.Errorf("%w: invalid email", domain.ErrInvalidArgument)
	}
	at := strings.IndexByte(email, '@')
	if at <= 0 || at == len(email)-1 || strings.Contains(email, " ") {
		return fmt.Errorf("%w: invalid email", domain.ErrInvalidArgument)
	}
	return nil
}

func validatePassword(password string) error {
	// Length is the only requirement that measurably helps (NIST 800-63B);
	// the 128 cap bounds Argon2id cost per attempt.
	if len(password) < 10 {
		return fmt.Errorf("%w: password must be at least 10 characters", domain.ErrInvalidArgument)
	}
	if len(password) > 128 {
		return fmt.Errorf("%w: password must be at most 128 characters", domain.ErrInvalidArgument)
	}
	return nil
}
