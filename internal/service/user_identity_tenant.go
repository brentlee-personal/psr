package service

import (
	"context"
	"ragflow/internal/common"
	"ragflow/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Strict-only tenant resolution; ordinary login and API-key paths are unchanged.
func resolveStrictNativeTenant(ctx context.Context, db *gorm.DB, user *entity.User, signedTenant string) (string, error) {
	if err := checkNativeAccount(user); err != nil { return "", err }
	if common.GetRAGFlowType() != "open source" {
		if signedTenant == "" { return "", ErrUserTokenUnverified }
		return resolveUserAuthTenant(ctx, user, signedTenant)
	}
	if db == nil || ctx.Err() != nil { return "", ErrUserTokenUnverified }
	if signedTenant == "" && (user.IsSuperuser == nil || *user.IsSuperuser) { return "", ErrUserTokenUnverified }
	var rows []struct {
		TenantID string `gorm:"column:tenant_id"`
		Membership *string `gorm:"column:membership_state"`
		Tenant *string `gorm:"column:tenant_state"`
	}
	// Include NULL/unknown states as uncertainty, not as fabricated uniqueness.
	q := db.Session(&gorm.Session{Logger: logger.Discard}).WithContext(ctx).
		Table("user_tenant AS m").Select("m.tenant_id, m.status AS membership_state, t.status AS tenant_state").
		Joins("LEFT JOIN tenant AS t ON t.id = m.tenant_id").Where("m.user_id = ?", user.ID).
		Where("(m.status IS NULL OR m.status NOT IN ?) AND (t.id IS NULL OR t.status IS NULL OR t.status <> ?)", []string{"0", "2", "3", "4", "5"}, "0")
	if signedTenant != "" { q = q.Where("m.tenant_id = ?", signedTenant) }
	if err := q.Limit(2).Scan(&rows).Error; err != nil { return "", ErrUserTokenUnverified }
	if len(rows) == 0 && signedTenant != "" { return "", ErrUserTokenDenied }
	if len(rows) != 1 { return "", ErrUserTokenUnverified }
	r := rows[0]
	if r.TenantID == "" || r.Membership == nil || r.Tenant == nil || *r.Membership != "1" || *r.Tenant != "1" { return "", ErrUserTokenUnverified }
	return r.TenantID, nil
}
