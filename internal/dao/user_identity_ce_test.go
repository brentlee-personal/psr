//go:build !ee

package dao

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Owned minimal native tables deliberately permit damaged historical rows.
func TestNativeUserIdentityObservationBoundsAndIndependentEvidence(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	for _, sql := range []string{
		`CREATE TABLE user (id TEXT, access_token TEXT, status TEXT, is_active TEXT, is_superuser BOOLEAN)`,
		`CREATE TABLE user_tenant (id TEXT, user_id TEXT, tenant_id TEXT, status TEXT)`,
		`CREATE TABLE tenant (id TEXT, status TEXT)`,
		`INSERT INTO user VALUES ('user','synthetic-current-access','1','1',false)`,
		`INSERT INTO tenant VALUES ('A','1'),('B','1'),('C','1'),('D','1')`,
		`INSERT INTO user_tenant VALUES ('a','user','A','1'),('b','user','B','1'),('c','user','C','1'),('d','user','D',NULL),('orphan','user','missing','1'),('duplicate','user','A','1')`,
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	reads := 0
	db.Callback().Row().After("gorm:row").Register("fixture:single-statement", func(*gorm.DB) { reads++ })
	observation, err := NewUserTenantDAO().ObserveNativeUserIdentity(t.Context(), db, "synthetic-current-access", "")
	if err != nil {
		t.Fatal(err)
	}
	if reads != 1 || observation.UserCount != 1 || len(observation.TenantIDs) != 2 || !observation.UnknownState || !observation.OrphanMembership || !observation.DuplicateMembership {
		t.Fatalf("incomplete bounded observation: %+v reads=%d", observation, reads)
	}
	signed, err := NewUserTenantDAO().ObserveNativeUserIdentity(t.Context(), db, "synthetic-current-access", "B")
	if err != nil || len(signed.TenantIDs) != 1 || signed.TenantIDs[0] != "B" || signed.UnknownState || signed.OrphanMembership || signed.DuplicateMembership {
		t.Fatalf("signed observation included unrelated evidence: %+v err=%v", signed, err)
	}
	if err := db.Exec(`INSERT INTO user VALUES ('other','synthetic-current-access','1','1',false),('third','synthetic-current-access','1','1',false)`).Error; err != nil {
		t.Fatal(err)
	}
	duplicate, err := NewUserTenantDAO().ObserveNativeUserIdentity(t.Context(), db, "synthetic-current-access", "B")
	if err != nil || duplicate.UserCount != 2 || duplicate.User != nil || len(duplicate.TenantIDs) > 2 {
		t.Fatalf("ambiguous current user lost: %+v err=%v", duplicate, err)
	}
}
