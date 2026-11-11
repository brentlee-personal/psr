//go:build !ee

package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"ragflow/internal/common"
	"ragflow/internal/entity"
)

const identityFixtureKey = "synthetic-test-signing-material-only"
const identityFixtureAccess = "00000000000000000000000000000001"

// These are owned, in-memory SQLite fixtures. The tenant hook below models
// only the resolved-tenant input to shared checks, not an EE implementation.
func newIdentityFixture(t *testing.T) (*UserTokenVerifier, *gorm.DB, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.AutoMigrate(&entity.User{}, &entity.Tenant{}, &entity.UserTenant{}); err != nil {
		t.Fatal(err)
	}
	active := "1"
	access := identityFixtureAccess
	for _, row := range []any{
		&entity.User{ID: "fixture-user", Email: "fixture@example.invalid", AccessToken: &access, Status: &active, IsActive: "1"},
		&entity.Tenant{ID: "fixture-tenant", Status: &active},
		&entity.UserTenant{ID: "fixture-member", UserID: "fixture-user", TenantID: "fixture-tenant", Status: &active},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	v := &UserTokenVerifier{source: userTokenSource{
		db:     func() *gorm.DB { return db },
		secret: func(context.Context) (string, error) { return identityFixtureKey, nil },
		tenant: func(_ context.Context, _ *entity.User, tenant string) (string, error) { return tenant, nil },
	}}
	token, err := common.DumpAccessToken(access, "fixture-tenant", identityFixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	return v, db, token
}

// Timestamp mutation always re-signs with an ephemeral synthetic fixture key.
// Production signing/verification continues to use the one native token core.
func fixtureTokenAt(token string, at time.Time) string {
	parts := strings.Split(token, ".")
	parts[3] = strconv.FormatInt(at.Unix(), 10)
	value := strings.Join(parts[:4], ".")
	mac := hmac.New(sha256.New, []byte(identityFixtureKey))
	mac.Write([]byte(value))
	return value + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestStrictNativeUserIdentityProofAndDeadline(t *testing.T) {
	v, _, token := newIdentityFixture(t)
	identity, err := v.VerifyUserToken(t.Context(), token, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := identity.Snapshot()
	if snapshot.UserID != "fixture-user" || snapshot.TenantID != "fixture-tenant" || snapshot.AuthType != "native_user_token" || snapshot.ExpiresAt.Sub(snapshot.IssuedAt) != 30*time.Minute {
		t.Fatalf("unexpected safe metadata: %+v", snapshot)
	}
	if err := v.CheckCurrentIdentity(t.Context(), identity); err != nil {
		t.Fatal(err)
	}
	native, err := v.VerifyUserToken(t.Context(), token, 48*time.Hour)
	if err != nil || native.Snapshot().ExpiresAt.Sub(native.Snapshot().IssuedAt) != 24*time.Hour {
		t.Fatal("policy extended native expiry")
	}
	if err := v.CheckCurrentIdentity(t.Context(), VerifiedUserIdentity{}); !errors.Is(err, ErrUserTokenDenied) {
		t.Fatal("zero proof accepted")
	}
	other, _, _ := newIdentityFixture(t)
	if err := other.CheckCurrentIdentity(t.Context(), identity); !errors.Is(err, ErrUserTokenDenied) {
		t.Fatal("cross issuer proof accepted")
	}
	encoded, _ := json.Marshal(identity)
	values := []string{string(encoded), fmt.Sprintf("%v %+v %#v", identity, identity, identity), fmt.Sprintf("%+v", identity.proof)}
	for _, value := range values {
		for _, secret := range []string{token, identityFixtureAccess, identityFixtureKey} {
			if strings.Contains(value, secret) {
				t.Fatal("proof leaked")
			}
		}
	}
	var restored VerifiedUserIdentity
	if err := json.Unmarshal(encoded, &restored); err == nil {
		t.Fatal("serialized proof restored")
	}
}

func TestStrictNativeUserIdentityRejectsCredentials(t *testing.T) {
	cases := map[string]func(string) string{
		"fake":         func(token string) string { return token + "x" },
		"expired":      func(token string) string { return fixtureTokenAt(token, time.Now().Add(-25*time.Hour)) },
		"future":       func(token string) string { return fixtureTokenAt(token, time.Now().Add(time.Hour)) },
		"customer-age": func(token string) string { return fixtureTokenAt(token, time.Now().Add(-31*time.Minute)) },
		"api-key":      func(string) string { return "ragflow-fake-api-key" },
		"beta":         func(string) string { return "0123456789abcdef0123456789abcdef" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v, _, token := newIdentityFixture(t)
			_, err := v.VerifyUserToken(t.Context(), mutate(token), 30*time.Minute)
			if !errors.Is(err, ErrUserTokenDenied) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestStrictNativeUserIdentityRechecksCurrentState(t *testing.T) {
	cases := map[string]func(*gorm.DB) error{
		"replacement": func(db *gorm.DB) error {
			return db.Model(&entity.User{}).Where("id = ?", "fixture-user").Update("access_token", "replacement-000000000000000000000").Error
		},
		"logout": func(db *gorm.DB) error {
			return db.Model(&entity.User{}).Where("id = ?", "fixture-user").Update("access_token", "INVALID_000000000000000000000000").Error
		},
		"disabled": func(db *gorm.DB) error {
			return db.Model(&entity.User{}).Where("id = ?", "fixture-user").Update("is_active", "0").Error
		},
		"status-disabled": func(db *gorm.DB) error {
			return db.Model(&entity.User{}).Where("id = ?", "fixture-user").Update("status", "0").Error
		},
		"membership-left": func(db *gorm.DB) error {
			return db.Model(&entity.UserTenant{}).Where("id = ?", "fixture-member").Update("status", entity.TenantMembershipStatusLeft).Error
		},
		"membership-removed": func(db *gorm.DB) error {
			return db.Where("id = ?", "fixture-member").Delete(&entity.UserTenant{}).Error
		},
		"tenant-disabled": func(db *gorm.DB) error {
			return db.Model(&entity.Tenant{}).Where("id = ?", "fixture-tenant").Update("status", "0").Error
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v, db, token := newIdentityFixture(t)
			proof, err := v.VerifyUserToken(t.Context(), token, 30*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err := mutate(db); err != nil {
				t.Fatal(err)
			}
			if err := v.CheckCurrentIdentity(t.Context(), proof); !errors.Is(err, ErrUserTokenDenied) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestStrictNativeUserIdentityUnknownSourcesAndCE(t *testing.T) {
	for _, stage := range []string{"key", "database", "tenant-resolver", "ce-empty", "nil-account-state", "nil-membership-state"} {
		t.Run(stage, func(t *testing.T) {
			v, db, token := newIdentityFixture(t)
			proof, err := v.VerifyUserToken(t.Context(), token, 30*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "key":
				v.source.secret = func(context.Context) (string, error) { return "", errors.New("backend-sensitive-details") }
			case "database":
				sqlDB, _ := db.DB()
				sqlDB.Close()
			case "tenant-resolver":
				v.source.tenant = func(context.Context, *entity.User, string) (string, error) {
					return "", errors.New("backend-sensitive-details")
				}
			case "ce-empty":
				v.source.tenant = resolveUserAuthTenant
			case "nil-account-state":
				db.Model(&entity.User{}).Where("id = ?", "fixture-user").Update("status", nil)
			case "nil-membership-state":
				db.Model(&entity.UserTenant{}).Where("id = ?", "fixture-member").Update("status", nil)
			}
			err = v.CheckCurrentIdentity(t.Context(), proof)
			if !errors.Is(err, ErrUserTokenUnverified) || strings.Contains(err.Error(), "backend-sensitive-details") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestStrictNativeUserIdentitySuppressesTokenSQLLogs(t *testing.T) {
	v, db, token := newIdentityFixture(t)
	var logs bytes.Buffer
	noisy := db.Session(&gorm.Session{Logger: logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Info})})
	v.source.db = func() *gorm.DB { return noisy }
	if err := db.Where("id = ?", "fixture-user").Delete(&entity.User{}).Error; err != nil {
		t.Fatal(err)
	}
	_, err := v.VerifyUserToken(t.Context(), token, 0)
	if !errors.Is(err, ErrUserTokenDenied) {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), identityFixtureAccess) {
		t.Fatal("access token leaked in SQL logger")
	}
}

func TestStrictNativeUserIdentityRejectsExpiryDuringStateRead(t *testing.T) {
	v, _, token := newIdentityFixture(t)
	// A two-second policy ensures the initial verification enters the hook.
	// The hook waits just past that same authenticated deadline.
	parts := strings.Split(token, ".")
	issued, _ := strconv.ParseInt(parts[3], 10, 64)
	deadline := time.Unix(issued, 0).Add(2 * time.Second)
	entered := false
	v.source.tenant = func(_ context.Context, _ *entity.User, tenant string) (string, error) {
		entered = true
		time.Sleep(time.Until(deadline) + 10*time.Millisecond)
		return tenant, nil
	}
	if _, err := v.VerifyUserToken(t.Context(), token, 2*time.Second); !errors.Is(err, ErrUserTokenDenied) {
		t.Fatalf("expired after state I/O: %v", err)
	}
	if !entered {
		t.Fatal("did not exercise authority delay")
	}
}

func TestStrictNativeUserIdentityRejectsCancellationDuringStateRead(t *testing.T) {
	v, _, token := newIdentityFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	v.source.tenant = func(_ context.Context, _ *entity.User, tenant string) (string, error) { cancel(); return tenant, nil }
	if _, err := v.VerifyUserToken(ctx, token, 30*time.Minute); !errors.Is(err, ErrUserTokenUnverified) {
		t.Fatalf("cancelled after state I/O: %v", err)
	}
}

func TestStrictNativeUserIdentityRechecksKeyAndAge(t *testing.T) {
	t.Run("key-rotation", func(t *testing.T) {
		v, _, token := newIdentityFixture(t)
		proof, err := v.VerifyUserToken(t.Context(), token, 30*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		v.source.secret = func(context.Context) (string, error) { return "rotated-synthetic-authority", nil }
		if err := v.CheckCurrentIdentity(t.Context(), proof); !errors.Is(err, ErrUserTokenDenied) {
			t.Fatalf("rotated key accepted: %v", err)
		}
	})
	t.Run("empty-key", func(t *testing.T) {
		v, _, token := newIdentityFixture(t)
		v.source.secret = func(context.Context) (string, error) { return "", nil }
		if _, err := v.VerifyUserToken(t.Context(), token, 0); !errors.Is(err, ErrUserTokenUnverified) {
			t.Fatal("empty key accepted")
		}
	})
	t.Run("negative-age", func(t *testing.T) {
		v, _, token := newIdentityFixture(t)
		if _, err := v.VerifyUserToken(t.Context(), token, -time.Second); !errors.Is(err, ErrUserTokenDenied) {
			t.Fatal("negative maxAge accepted")
		}
	})
	t.Run("subsecond-expiry", func(t *testing.T) {
		v, _, token := newIdentityFixture(t)
		if _, err := v.VerifyUserToken(t.Context(), token, time.Nanosecond); !errors.Is(err, ErrUserTokenDenied) {
			t.Fatal("subsecond expired policy accepted")
		}
	})
}
