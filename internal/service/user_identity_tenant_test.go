//go:build !ee

package service

import (
	"errors"
	"testing"
	"time"
	"ragflow/internal/common"
	"ragflow/internal/entity"
	"gorm.io/gorm"
)

// Owned native codec/SQLite fixtures, not a real credential or cloud result.
func strictCEFixture(t *testing.T) (*UserTokenVerifier,*gorm.DB,string) {
	v,db,_:=newIdentityFixture(t)
	if err:=db.Model(&entity.User{}).Where("id = ?","fixture-user").Update("is_superuser",false).Error;err!=nil{t.Fatal(err)}
	v.source.tenant=resolveStrictNativeTenant
	token,err:=common.DumpAccessToken(identityFixtureAccess,"",identityFixtureKey)
	if err!=nil{t.Fatal(err)}
	return v,db,token
}

func addSecondNativeTenant(t *testing.T,db *gorm.DB) {
	t.Helper();active:="1"
	for _,row:=range []any{&entity.Tenant{ID:"fixture-second",Status:&active},&entity.UserTenant{ID:"fixture-member-second",UserID:"fixture-user",TenantID:"fixture-second",Status:&active}} {
		if err:=db.Create(row).Error;err!=nil{t.Fatal(err)}
	}
}

func TestStrictCEEmptyTenantUsesNativeProofAndRejectsDrift(t *testing.T) {
	v,db,token:=strictCEFixture(t)
	parsed,err:=common.ExtractAccessTokenContext(token,identityFixtureKey);if err!=nil{t.Fatal(err)}
	proof,err:=v.VerifyUserToken(t.Context(),token,30*time.Minute);if err!=nil{t.Fatal(err)}
	snapshot:=proof.Snapshot()
	if snapshot.TenantID!="fixture-tenant" || !snapshot.IssuedAt.Equal(parsed.IssuedAt) || !snapshot.ExpiresAt.Equal(parsed.IssuedAt.Add(30*time.Minute)) {t.Fatal("native identity/times not preserved")}
	if err:=v.CheckCurrentIdentity(t.Context(),proof);err!=nil{t.Fatal(err)}
	addSecondNativeTenant(t,db)
	if err:=v.CheckCurrentIdentity(t.Context(),proof);!errors.Is(err,ErrUserTokenUnverified){t.Fatal("ambiguous membership accepted")}
	if err:=db.Where("id = ?","fixture-member").Delete(&entity.UserTenant{}).Error;err!=nil{t.Fatal(err)}
	if err:=v.CheckCurrentIdentity(t.Context(),proof);!errors.Is(err,ErrUserTokenDenied){t.Fatal("old proof silently changed tenant")}
}

func TestStrictCEEmptyTenantRejectsUnknownOrAdmin(t *testing.T) {
	for _,name:=range []string{"null-membership","null-tenant","superuser"} {
		t.Run(name,func(t *testing.T){
			v,db,token:=strictCEFixture(t)
			var err error
			switch name {
			case "null-membership":err=db.Model(&entity.UserTenant{}).Where("id = ?","fixture-member").Update("status",nil).Error
			case "null-tenant":err=db.Model(&entity.Tenant{}).Where("id = ?","fixture-tenant").Update("status",nil).Error
			case "superuser":err=db.Model(&entity.User{}).Where("id = ?","fixture-user").Update("is_superuser",true).Error
			}
			if err!=nil{t.Fatal(err)}
			if _,err:=v.VerifyUserToken(t.Context(),token,30*time.Minute);!errors.Is(err,ErrUserTokenUnverified){t.Fatal("unknown/admin tenant accepted")}
		})
	}
}

func TestStrictCESignedTenantCannotFallback(t *testing.T) {
	v,db,_:=strictCEFixture(t);addSecondNativeTenant(t,db)
	token,err:=common.DumpAccessToken(identityFixtureAccess,"fixture-tenant",identityFixtureKey);if err!=nil{t.Fatal(err)}
	proof,err:=v.VerifyUserToken(t.Context(),token,30*time.Minute);if err!=nil{t.Fatal(err)}
	if err:=db.Model(&entity.UserTenant{}).Where("id = ?","fixture-member").Update("status","5").Error;err!=nil{t.Fatal(err)}
	if err:=v.CheckCurrentIdentity(t.Context(),proof);!errors.Is(err,ErrUserTokenDenied){t.Fatal("signed tenant fell back to another membership")}
}
