// Package authn exposes strict native user authentication for applications
// embedding the initialized PSR runtime. It has no API-key or cookie fallback.
package authn

import (
	"context"
	"net/http"
	"strings"
	"time"

	"ragflow/internal/service"
)

var (
	ErrDenied     = service.ErrUserTokenDenied
	ErrUnverified = service.ErrUserTokenUnverified
)

type VerifiedIdentity = service.VerifiedUserIdentity
type IdentitySnapshot = service.UserIdentitySnapshot

// UserTokenPolicy can shorten, but never extend, the native token lifetime.
// MaxAge zero uses native expiry. Use 30*time.Minute for a 30-minute policy.
type UserTokenPolicy struct{ MaxAge time.Duration }

// Verifier owns the in-process proofs it issues. Keep one instance across all
// request stages; a different instance cannot recheck its proofs.
type Verifier struct{ native *service.UserTokenVerifier }

func NewVerifier() *Verifier { return &Verifier{native: service.NewUserTokenVerifier()} }

// VerifyUserRequest trusts only one Authorization: Bearer native-user-token
// header. Request body, query parameters, cookies and API-key headers cannot
// supply identity or tenant context.
func (v *Verifier) VerifyUserRequest(ctx context.Context, request *http.Request, policy UserTokenPolicy) (VerifiedIdentity, error) {
	if v == nil || v.native == nil {
		return VerifiedIdentity{}, ErrUnverified
	}
	if request == nil || len(request.Header.Values("Authorization")) != 1 {
		return VerifiedIdentity{}, ErrDenied
	}
	authorization := request.Header.Get("Authorization")
	fields := strings.Fields(authorization)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return VerifiedIdentity{}, ErrDenied
	}
	return v.native.VerifyUserToken(ctx, "Bearer "+fields[1], policy.MaxAge)
}

func (v *Verifier) CheckCurrentIdentity(ctx context.Context, identity VerifiedIdentity) error {
	if v == nil || v.native == nil {
		return ErrUnverified
	}
	return v.native.CheckCurrentIdentity(ctx, identity)
}
