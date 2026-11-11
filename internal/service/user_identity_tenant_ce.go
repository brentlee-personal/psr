//go:build !ee

package service

import (
	"context"

	"ragflow/internal/common"
	"ragflow/internal/dao"

	"gorm.io/gorm"
)

func init() {
	// Compile-time CE wiring only. There is no public authority setter. EE does
	// not compile this file and retains its existing native edition path/symbols.
	nativeUserIdentityIsCE = true
	nativeUserIdentityObserver = observeCENativeUserIdentity
}

func observeCENativeUserIdentity(ctx context.Context, db *gorm.DB, token *common.AccessTokenContext) (*AuthIdentity, error) {
	observed, err := dao.NewUserTenantDAO().ObserveNativeUserIdentity(ctx, db, token.AccessToken, token.TenantID)
	if err != nil {
		return nil, ErrUserTokenUnverified
	}
	if observed.UserCount == 0 {
		return nil, ErrUserTokenDenied
	}
	if observed.UserCount != 1 {
		return nil, ErrUserTokenUnverified
	}
	user := observed.User
	if user == nil || user.ID == "" || user.Status == nil || user.IsActive == "" || user.IsSuperuser == nil {
		return nil, ErrUserTokenUnverified
	}
	if (*user.Status != "0" && *user.Status != "1") || (user.IsActive != "0" && user.IsActive != "1") {
		return nil, ErrUserTokenUnverified
	}
	if *user.Status == "0" || user.IsActive == "0" || *user.IsSuperuser {
		return nil, ErrUserTokenDenied
	}
	if observed.UnknownState || observed.DuplicateMembership {
		return nil, ErrUserTokenUnverified
	}
	if observed.OrphanMembership {
		if token.TenantID != "" {
			return nil, ErrUserTokenDenied
		}
		return nil, ErrUserTokenUnverified
	}
	if len(observed.TenantIDs) != 1 {
		if token.TenantID != "" && len(observed.TenantIDs) == 0 {
			return nil, ErrUserTokenDenied
		}
		return nil, ErrUserTokenUnverified
	}
	tenantID := observed.TenantIDs[0]
	if tenantID == "" {
		return nil, ErrUserTokenUnverified
	}
	if token.TenantID != "" && token.TenantID != tenantID {
		return nil, ErrUserTokenDenied
	}
	return &AuthIdentity{User: user, TenantID: tenantID}, nil
}
