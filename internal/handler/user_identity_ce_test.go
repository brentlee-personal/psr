//go:build !ee

package handler

import (
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ragflow/internal/common"
	"ragflow/internal/dao"
	"ragflow/internal/entity"
	"ragflow/internal/permission"
	"ragflow/internal/server"
	"ragflow/pkg/authn"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCENativeIssuerToStrictResolver(t *testing.T) {
	// Native runtime configuration is process-global. Keep this fixture in a
	// child test process so no other handler test inherits its signing authority.
	const marker = "PSR_CE_IDENTITY_ISSUER_FIXTURE"
	if os.Getenv(marker) != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, "-test.run=^TestCENativeIssuerToStrictResolver$", "-test.v")
		cmd.Env = append(os.Environ(), marker+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native issuer fixture failed: %v\n%s", err, output)
		}
		return
	}
	const key = "synthetic-ce-issuer-key-only"
	t.Setenv("RAGFLOW_SECRET_KEY", key)
	path := filepath.Join(t.TempDir(), "fixture.yaml")
	if err := os.WriteFile(path, []byte("secret_key: "+key+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := server.Init(path); err != nil {
		t.Fatal(err)
	}
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
	access, active, ordinary := "00000000000000000000000000feedce", "1", false
	user := &entity.User{ID: "issuer-user", Email: "issuer@example.invalid", AccessToken: &access, Status: &active, IsActive: active, IsSuperuser: &ordinary}
	for _, row := range []any{user, &entity.Tenant{ID: "actual-native-tenant", Status: &active}, &entity.UserTenant{ID: "actual-member", UserID: user.ID, TenantID: "actual-native-tenant", Role: "normal", Status: &active}} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	oldDB := dao.DB
	dao.DB = db
	t.Cleanup(func() { dao.DB = oldDB })
	// Call the actual CE login issuer, not DumpAccessToken with a chosen tenant.
	token, err := issueUserAccessToken(user, key)
	if err != nil {
		t.Fatal(err)
	}
	native, err := common.ExtractAccessTokenContext(token, key)
	if err != nil || native.TenantID != "" {
		t.Fatal("CE issuer did not produce its native empty-tenant credential")
	}
	request := httptest.NewRequest("POST", "/?tenant_id=forged", strings.NewReader(`{"user_id":"forged","tenant_id":"forged"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Cookie", "tenant_id=forged")
	request.Header.Set("X-API-Key", "forged")
	ctx := permission.WithIdentity(t.Context(), "forged", "forged")
	verifier := authn.NewVerifier()
	proof, err := verifier.VerifyUserRequest(ctx, request, authn.UserTokenPolicy{MaxAge: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := proof.Snapshot()
	if snapshot.UserID != user.ID || snapshot.TenantID != "actual-native-tenant" || !snapshot.IssuedAt.Equal(native.IssuedAt) || !snapshot.ExpiresAt.Equal(native.IssuedAt.Add(30*time.Minute)) {
		t.Fatalf("wrong native resolved metadata: %+v", snapshot)
	}
	if err := verifier.CheckCurrentIdentity(t.Context(), proof); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("is_superuser", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := verifier.CheckCurrentIdentity(t.Context(), proof); !errors.Is(err, authn.ErrDenied) {
		t.Fatalf("CE admin promotion accepted: %v", err)
	}
}
