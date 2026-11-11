//go:build !ee

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"ragflow/internal/common"
	"ragflow/internal/entity"
	"ragflow/internal/permission"

	"gorm.io/gorm"
)

func assertCEIdentityError(t *testing.T, v *UserTokenVerifier, token string, want error) {
	t.Helper()
	identity, err := v.VerifyUserToken(t.Context(), token, 30*time.Minute)
	if !errors.Is(err, want) || identity.proof != nil || identity.Snapshot() != (UserIdentitySnapshot{}) {
		t.Fatalf("want zero proof and %v; got %v, snapshot=%+v", want, err, identity.Snapshot())
	}
}

func ceFixtureToken(t *testing.T, tenant string) string {
	t.Helper()
	token, err := common.DumpAccessToken(identityFixtureAccess, tenant, identityFixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func addCEFixtureMembership(t *testing.T, db *gorm.DB, id, tenant string, status *string, tenantStatus *string, withTenant bool) {
	t.Helper()
	if withTenant {
		if err := db.Create(&entity.Tenant{ID: tenant, Status: tenantStatus}).Error; err != nil {
			t.Fatal(err)
		}
		// GORM's defaults fill nil on creation; explicitly retain unknown authority.
		if tenantStatus == nil {
			if err := db.Model(&entity.Tenant{}).Where("id = ?", tenant).Update("status", nil).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Create(&entity.UserTenant{ID: id, UserID: "fixture-user", TenantID: tenant, Role: "owner", Status: status}).Error; err != nil {
		t.Fatal(err)
	}
	if status == nil {
		if err := db.Model(&entity.UserTenant{}).Where("id = ?", id).Update("status", nil).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCENativeTenantEmptyAndSignedSelection(t *testing.T) {
	for _, signed := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "signed"}[signed], func(t *testing.T) {
			v, db, _ := newIdentityFixture(t)
			tenant := ""
			if signed {
				tenant = "fixture-tenant"
			}
			token := ceFixtureToken(t, tenant)
			current, err := common.ExtractAccessTokenContext(token, identityFixtureKey)
			if err != nil {
				t.Fatal(err)
			}
			// Application identity/scope has no authority over native resolution.
			ctx := permission.WithIdentity(t.Context(), "forged-user", "forged-tenant")
			identity, err := v.VerifyUserToken(ctx, token, 30*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := identity.Snapshot()
			if snapshot.TenantID != "fixture-tenant" || snapshot.UserID != "fixture-user" || !snapshot.IssuedAt.Equal(current.IssuedAt) || !snapshot.ExpiresAt.Equal(current.IssuedAt.Add(30*time.Minute)) {
				t.Fatalf("wrong resolved snapshot: %+v", snapshot)
			}
			if identity.proof.authorization != token || identity.proof.maxAge != 30*time.Minute {
				t.Fatal("original credential/policy changed")
			}
			// Role changes do not generate identity grants or require ownership.
			if err := db.Model(&entity.UserTenant{}).Where("id = ?", "fixture-member").Update("role", "normal").Error; err != nil {
				t.Fatal(err)
			}
			if err := v.CheckCurrentIdentity(t.Context(), identity); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCENativeTenantCandidateCompleteness(t *testing.T) {
	active, disabled, left, unknown := "1", "0", entity.TenantMembershipStatusLeft, "99"
	cases := []struct {
		name          string
		mutate        func(*testing.T, *gorm.DB)
		empty, signed error
	}{
		{"zero", func(t *testing.T, db *gorm.DB) { db.Where("id = ?", "fixture-member").Delete(&entity.UserTenant{}) }, ErrUserTokenUnverified, ErrUserTokenDenied},
		{"two", func(t *testing.T, db *gorm.DB) {
			addCEFixtureMembership(t, db, "other-member", "other-tenant", &active, &active, true)
		}, ErrUserTokenUnverified, nil},
		{"valid-plus-null-member", func(t *testing.T, db *gorm.DB) {
			addCEFixtureMembership(t, db, "other-member", "other-tenant", nil, &active, true)
		}, ErrUserTokenUnverified, nil},
		{"valid-plus-unknown-member", func(t *testing.T, db *gorm.DB) {
			addCEFixtureMembership(t, db, "other-member", "other-tenant", &unknown, &active, true)
		}, ErrUserTokenUnverified, nil},
		{"valid-plus-null-tenant", func(t *testing.T, db *gorm.DB) {
			addCEFixtureMembership(t, db, "other-member", "other-tenant", &active, nil, true)
		}, ErrUserTokenUnverified, nil},
		{"valid-plus-unknown-tenant", func(t *testing.T, db *gorm.DB) {
			addCEFixtureMembership(t, db, "other-member", "other-tenant", &active, &unknown, true)
		}, ErrUserTokenUnverified, nil},
		{"valid-plus-orphan", func(t *testing.T, db *gorm.DB) {
			addCEFixtureMembership(t, db, "other-member", "other-tenant", &active, nil, false)
		}, ErrUserTokenUnverified, nil},
		{"valid-plus-left", func(t *testing.T, db *gorm.DB) {
			addCEFixtureMembership(t, db, "other-member", "other-tenant", &left, nil, false)
		}, nil, nil},
		{"valid-plus-disabled", func(t *testing.T, db *gorm.DB) {
			addCEFixtureMembership(t, db, "other-member", "other-tenant", &active, &disabled, true)
		}, nil, nil},
		{"null-selected-member", func(t *testing.T, db *gorm.DB) {
			db.Model(&entity.UserTenant{}).Where("id = ?", "fixture-member").Update("status", nil)
		}, ErrUserTokenUnverified, ErrUserTokenUnverified},
		{"unknown-selected-member", func(t *testing.T, db *gorm.DB) {
			db.Model(&entity.UserTenant{}).Where("id = ?", "fixture-member").Update("status", unknown)
		}, ErrUserTokenUnverified, ErrUserTokenUnverified},
		{"null-selected-tenant", func(t *testing.T, db *gorm.DB) {
			db.Model(&entity.Tenant{}).Where("id = ?", "fixture-tenant").Update("status", nil)
		}, ErrUserTokenUnverified, ErrUserTokenUnverified},
		{"unknown-selected-tenant", func(t *testing.T, db *gorm.DB) {
			db.Model(&entity.Tenant{}).Where("id = ?", "fixture-tenant").Update("status", unknown)
		}, ErrUserTokenUnverified, ErrUserTokenUnverified},
		{"orphan-selected-tenant", func(t *testing.T, db *gorm.DB) { db.Where("id = ?", "fixture-tenant").Delete(&entity.Tenant{}) }, ErrUserTokenUnverified, ErrUserTokenDenied},
		{"disabled-selected-tenant", func(t *testing.T, db *gorm.DB) {
			db.Model(&entity.Tenant{}).Where("id = ?", "fixture-tenant").Update("status", disabled)
		}, ErrUserTokenUnverified, ErrUserTokenDenied},
		{"duplicate-selected-membership", func(t *testing.T, db *gorm.DB) {
			if err := db.Migrator().DropIndex(&entity.UserTenant{}, "idx_user_tenant_user_tenant"); err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&entity.UserTenant{ID: "duplicate", UserID: "fixture-user", TenantID: "fixture-tenant", Status: &active}).Error; err != nil {
				t.Fatal(err)
			}
		}, ErrUserTokenUnverified, ErrUserTokenUnverified},
		{"duplicate-unselected-membership", func(t *testing.T, db *gorm.DB) {
			addCEFixtureMembership(t, db, "other-member", "other-tenant", &left, &active, true)
			if err := db.Migrator().DropIndex(&entity.UserTenant{}, "idx_user_tenant_user_tenant"); err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&entity.UserTenant{ID: "duplicate", UserID: "fixture-user", TenantID: "other-tenant", Status: &left}).Error; err != nil {
				t.Fatal(err)
			}
		}, ErrUserTokenUnverified, nil},
		{"duplicate-token-user", func(t *testing.T, db *gorm.DB) {
			access := identityFixtureAccess
			ordinary := false
			if err := db.Create(&entity.User{ID: "duplicate-user", Email: "duplicate@example.invalid", AccessToken: &access, Status: &active, IsActive: "1", IsSuperuser: &ordinary}).Error; err != nil {
				t.Fatal(err)
			}
		}, ErrUserTokenUnverified, ErrUserTokenUnverified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, signed := range []bool{false, true} {
				t.Run(map[bool]string{false: "empty", true: "signed"}[signed], func(t *testing.T) {
					v, db, _ := newIdentityFixture(t)
					tc.mutate(t, db)
					tenant := ""
					want := tc.empty
					if signed {
						tenant = "fixture-tenant"
						want = tc.signed
					}
					token := ceFixtureToken(t, tenant)
					if want != nil {
						assertCEIdentityError(t, v, token, want)
					} else {
						identity, err := v.VerifyUserToken(t.Context(), token, 0)
						if err != nil || identity.Snapshot().TenantID != "fixture-tenant" {
							t.Fatalf("valid native relation rejected: %v", err)
						}
					}
				})
			}
		})
	}
	for _, status := range []string{"0", "2", "3", "4", "5"} {
		t.Run("only-known-nonjoined-"+status, func(t *testing.T) {
			v, db, _ := newIdentityFixture(t)
			if err := db.Model(&entity.UserTenant{}).Where("id = ?", "fixture-member").Update("status", status).Error; err != nil {
				t.Fatal(err)
			}
			assertCEIdentityError(t, v, ceFixtureToken(t, ""), ErrUserTokenUnverified)
			assertCEIdentityError(t, v, ceFixtureToken(t, "fixture-tenant"), ErrUserTokenDenied)
		})
	}
}

func TestCENativeTenantRequiresExplicitOrdinaryUser(t *testing.T) {
	for _, signed := range []bool{false, true} {
		for _, state := range []string{"admin", "unknown-admin", "unknown-status", "unknown-active", "null-status", "missing-user-id"} {
			t.Run(map[bool]string{false: "empty-", true: "signed-"}[signed]+state, func(t *testing.T) {
				v, db, _ := newIdentityFixture(t)
				want := ErrUserTokenUnverified
				switch state {
				case "admin":
					db.Model(&entity.User{}).Where("id = ?", "fixture-user").Update("is_superuser", true)
					want = ErrUserTokenDenied
				case "unknown-admin":
					db.Model(&entity.User{}).Where("id = ?", "fixture-user").Update("is_superuser", nil)
				case "unknown-status":
					db.Model(&entity.User{}).Where("id = ?", "fixture-user").Update("status", "9")
				case "unknown-active":
					db.Model(&entity.User{}).Where("id = ?", "fixture-user").Update("is_active", "9")
				case "null-status":
					db.Model(&entity.User{}).Where("id = ?", "fixture-user").Update("status", nil)
				case "missing-user-id":
					db.Model(&entity.User{}).Where("id = ?", "fixture-user").Update("id", "")
				}
				tenant := ""
				if signed {
					tenant = "fixture-tenant"
				}
				assertCEIdentityError(t, v, ceFixtureToken(t, tenant), want)
			})
		}
	}
}

func TestCENativeTenantRecheckPreservesOriginalMode(t *testing.T) {
	active := "1"
	for _, signed := range []bool{false, true} {
		for _, change := range []string{"add-B", "replace-A-with-B", "promote-admin"} {
			t.Run(map[bool]string{false: "empty-", true: "signed-"}[signed]+change, func(t *testing.T) {
				v, db, _ := newIdentityFixture(t)
				tenant := ""
				if signed {
					tenant = "fixture-tenant"
				}
				identity, err := v.VerifyUserToken(t.Context(), ceFixtureToken(t, tenant), 30*time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				want := ErrUserTokenDenied
				switch change {
				case "add-B":
					addCEFixtureMembership(t, db, "other-member", "other-tenant", &active, &active, true)
					if signed {
						want = nil
					} else {
						want = ErrUserTokenUnverified
					}
				case "replace-A-with-B":
					db.Model(&entity.UserTenant{}).Where("id = ?", "fixture-member").Update("status", entity.TenantMembershipStatusLeft)
					addCEFixtureMembership(t, db, "other-member", "other-tenant", &active, &active, true)
				case "promote-admin":
					db.Model(&entity.User{}).Where("id = ?", "fixture-user").Update("is_superuser", true)
				}
				err = v.CheckCurrentIdentity(t.Context(), identity)
				if !errors.Is(err, want) {
					t.Fatalf("want %v got %v", want, err)
				}
			})
		}
	}
}

func TestCENativeTenantOneDatabaseObservation(t *testing.T) {
	v, db, token := newIdentityFixture(t)
	other, otherDB, _ := newIdentityFixture(t)
	_ = other
	if err := otherDB.Model(&entity.User{}).Where("id = ?", "fixture-user").Update("is_active", "0").Error; err != nil {
		t.Fatal(err)
	}
	calls, reads := 0, 0
	v.source.db = func() *gorm.DB {
		calls++
		if calls == 1 {
			return db
		}
		return otherDB
	}
	db.Callback().Row().After("gorm:row").Register("fixture:observe", func(*gorm.DB) { reads++ })
	proof, err := v.VerifyUserToken(t.Context(), token, 0)
	if err != nil || proof.proof == nil || calls != 1 || reads != 1 {
		t.Fatalf("observation split: db=%d statements=%d err=%v", calls, reads, err)
	}
	if err := v.CheckCurrentIdentity(t.Context(), proof); !errors.Is(err, ErrUserTokenDenied) {
		t.Fatalf("current DB not rechecked: %v", err)
	}
}

func TestCENativeTenantCancellationBeforeRead(t *testing.T) {
	v, _, token := newIdentityFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	proof, err := v.VerifyUserToken(ctx, token, 0)
	if !errors.Is(err, ErrUserTokenUnverified) || proof.proof != nil {
		t.Fatalf("cancelled authority accepted: %v", err)
	}
}
