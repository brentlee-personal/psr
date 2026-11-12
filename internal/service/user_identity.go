package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"ragflow/internal/common"
	"ragflow/internal/dao"
	"ragflow/internal/engine/kvrocks"
	"ragflow/internal/entity"
	"ragflow/internal/permission"
	"ragflow/internal/server"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ErrUserTokenDenied is a known invalid credential or inactive identity.
var ErrUserTokenDenied = errors.New("native user identity denied")

// ErrUserTokenUnverified means authoritative state could not be established.
var ErrUserTokenUnverified = errors.New("native user identity unverified")

// userTokenSource reuses the native token parser, DAO and edition resolver.
// Applications cannot replace its native signing or identity authorities.
type userTokenSource struct {
	db     func() *gorm.DB
	secret func(context.Context) (string, error)
	tenant func(context.Context, *gorm.DB, *entity.User, string) (string, error)
}

type signingKeyReader struct{ client *kvrocks.Client }

func (r signingKeyReader) Get(ctx context.Context, key string) (string, error) {
	return r.client.GetSensitive(ctx, key)
}

func newUserTokenSource() userTokenSource {
	return userTokenSource{
		db: func() *gorm.DB { return dao.DB },
		secret: func(ctx context.Context) (string, error) {
			var store server.SigningKeyReader
			if client := kvrocks.Get(); client != nil {
				store = signingKeyReader{client}
			}
			return server.GetCurrentSigningKey(ctx, store)
		},
		tenant: resolveStrictNativeTenant,
	}
}

func (s userTokenSource) resolve(ctx context.Context, authorization string, db *gorm.DB) (*AuthIdentity, *common.AccessTokenContext, error) {
	if ctx.Err() != nil {
		return nil, nil, ErrUserTokenUnverified
	}
	secret, err := s.secret(ctx)
	if err != nil || secret == "" {
		return nil, nil, ErrUserTokenUnverified
	}
	token, err := common.ExtractAccessTokenContext(authorization, secret)
	if err != nil || len(token.AccessToken) < 32 {
		return nil, nil, ErrUserTokenDenied
	}
	if db == nil {
		return nil, nil, ErrUserTokenUnverified
	}
	// A failed token lookup must never log the access_token SQL predicate.
	db = db.Session(&gorm.Session{Logger: logger.Discard})
	user, err := dao.NewUserDAO().GetByAccessToken(ctx, db, token.AccessToken)
	if err != nil {
		return nil, nil, userTokenStateError(err)
	}
	return &AuthIdentity{User: user, TenantID: token.TenantID}, token, nil
}

func userTokenStateError(err error) error {
	switch {
	case errors.Is(err, ErrUserTokenDenied):
		return ErrUserTokenDenied
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, permission.ErrMembershipNotFound), errors.Is(err, permission.ErrPermissionDenied), errors.Is(err, permission.ErrUnauthenticated):
		return ErrUserTokenDenied
	default:
		return ErrUserTokenUnverified
	}
}

// UserIdentitySnapshot contains only safe native identity metadata. A snapshot
// is not a proof and cannot be used to restore or construct an identity.
type UserIdentitySnapshot struct {
	UserID    string    `json:"user_id"`
	TenantID  string    `json:"tenant_id"`
	AuthType  string    `json:"auth_type"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	CheckedAt time.Time `json:"checked_at"`
}

// VerifiedUserIdentity carries an issuer-bound, in-process native proof. Its
// fields are private and its JSON and formatting deliberately omit the proof.
type VerifiedUserIdentity struct {
	snapshot UserIdentitySnapshot
	proof    *userTokenProof
}

type userTokenProof struct {
	issuer        *UserTokenVerifier
	authorization string
	maxAge        time.Duration
}

func (i VerifiedUserIdentity) Snapshot() UserIdentitySnapshot { return i.snapshot }
func (i VerifiedUserIdentity) Format(f fmt.State, _ rune) {
	io.WriteString(f, "VerifiedUserIdentity{proof:redacted}")
}
func (p userTokenProof) Format(f fmt.State, _ rune) { io.WriteString(f, "native-user-proof:redacted") }
func (i VerifiedUserIdentity) MarshalJSON() ([]byte, error) {
	return []byte(`{"native_identity":"redacted"}`), nil
}
func (i *VerifiedUserIdentity) UnmarshalJSON([]byte) error { return ErrUserTokenDenied }

// UserTokenVerifier uses the same signature, current-token DAO, and edition
// tenant resolver as the native user authentication path.
type UserTokenVerifier struct{ source userTokenSource }

func NewUserTokenVerifier() *UserTokenVerifier {
	return &UserTokenVerifier{source: newUserTokenSource()}
}

// VerifyUserToken verifies only a native signed user credential. maxAge zero
// selects native expiry; positive values can only shorten it.
func (v *UserTokenVerifier) VerifyUserToken(ctx context.Context, authorization string, maxAge time.Duration) (VerifiedUserIdentity, error) {
	if v == nil || v.source.db == nil {
		return VerifiedUserIdentity{}, ErrUserTokenUnverified
	}
	if maxAge < 0 {
		return VerifiedUserIdentity{}, ErrUserTokenDenied
	}
	db := v.source.db()
	identity, token, err := v.source.resolve(ctx, authorization, db)
	if err != nil {
		return VerifiedUserIdentity{}, err
	}
	now := time.Now().UTC()
	expiry := token.ExpiresAt
	if maxAge > 0 && token.IssuedAt.Add(maxAge).Before(expiry) {
		expiry = token.IssuedAt.Add(maxAge)
	}
	if !now.Before(expiry) {
		return VerifiedUserIdentity{}, ErrUserTokenDenied
	}
	if err := checkNativeAccount(identity.User); err != nil {
		return VerifiedUserIdentity{}, err
	}
	// Reject known invalid native account/member state before the edition hook:
	// downstream hooks may report such denials with opaque errors.
	tenantID, err := v.source.tenant(ctx, db, identity.User, token.TenantID)
	if err != nil {
		return VerifiedUserIdentity{}, userTokenStateError(err)
	}
	if tenantID == "" {
		return VerifiedUserIdentity{}, ErrUserTokenUnverified
	}
	if token.TenantID != "" && tenantID != token.TenantID {
		return VerifiedUserIdentity{}, ErrUserTokenDenied
	}

	identity.TenantID = tenantID
	if err := v.checkIdentityState(ctx, identity, db); err != nil { return VerifiedUserIdentity{}, err }

	// Authority reads may block past the deadline or ignore cancellation. Only
	// the completion time can establish a currently usable identity.
	if ctx.Err() != nil {
		return VerifiedUserIdentity{}, ErrUserTokenUnverified
	}
	now = time.Now().UTC()
	if !now.Before(expiry) {
		return VerifiedUserIdentity{}, ErrUserTokenDenied
	}

	return VerifiedUserIdentity{
		snapshot: UserIdentitySnapshot{UserID: identity.User.ID, TenantID: identity.TenantID, AuthType: "native_user_token", IssuedAt: token.IssuedAt, ExpiresAt: expiry, CheckedAt: now},
		proof:    &userTokenProof{issuer: v, authorization: authorization, maxAge: maxAge},
	}, nil
}

func (v *UserTokenVerifier) checkIdentityState(ctx context.Context, identity *AuthIdentity, db *gorm.DB) error {
	user := identity.User
	if err := checkNativeAccount(user); err != nil { return err }
	if identity.TenantID == "" {
		return ErrUserTokenUnverified
	}
	if db == nil {
		return ErrUserTokenUnverified
	}
	db = db.Session(&gorm.Session{Logger: logger.Discard}).WithContext(ctx)
	var tenant entity.Tenant
	if err := db.Take(&tenant, "id = ?", identity.TenantID).Error; err != nil {
		return userTokenStateError(err)
	}
	if tenant.Status == nil {
		return ErrUserTokenUnverified
	}
	if *tenant.Status != string(entity.StatusValid) {
		return ErrUserTokenDenied
	}
	var membership entity.UserTenant
	if err := db.Where("user_id = ? AND tenant_id = ?", user.ID, identity.TenantID).Take(&membership).Error; err != nil {
		return userTokenStateError(err)
	}
	if membership.Status == nil {
		return ErrUserTokenUnverified
	}
	if *membership.Status != entity.TenantMembershipStatusJoined {
		return ErrUserTokenDenied
	}
	return nil
}

// CheckCurrentIdentity rechecks the original native credential with this
// issuer and the original age policy. A user/tenant snapshot alone is rejected.
func (v *UserTokenVerifier) CheckCurrentIdentity(ctx context.Context, identity VerifiedUserIdentity) error {
	if v == nil || identity.proof == nil || identity.proof.issuer != v {
		return ErrUserTokenDenied
	}
	current, err := v.VerifyUserToken(ctx, identity.proof.authorization, identity.proof.maxAge)
	if err != nil {
		return err
	}
	if current.snapshot.UserID != identity.snapshot.UserID || current.snapshot.TenantID != identity.snapshot.TenantID || !current.snapshot.IssuedAt.Equal(identity.snapshot.IssuedAt) || !current.snapshot.ExpiresAt.Equal(identity.snapshot.ExpiresAt) {
		return ErrUserTokenDenied
	}
	return nil
}

func checkNativeAccount(user *entity.User) error {
	if user == nil || user.ID == "" {
		return ErrUserTokenUnverified
	}
	if user.Status == nil || user.IsActive == "" {
		return ErrUserTokenUnverified
	}
	if *user.Status != "1" || user.IsActive != "1" {
		return ErrUserTokenDenied
	}
	return nil
}
