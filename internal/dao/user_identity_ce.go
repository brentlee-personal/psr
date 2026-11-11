//go:build !ee

package dao

import (
	"context"

	"ragflow/internal/entity"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// NativeUserIdentityObservation is the bounded result of one read-only native
// database statement. It contains no credential and never selects a default.
// Counts/evidence cover all relevant rows, not just the two returned candidates.
type NativeUserIdentityObservation struct {
	UserCount           int64
	User                *entity.User
	TenantIDs           []string
	UnknownState        bool
	DuplicateMembership bool
	OrphanMembership    bool
}

// ObserveNativeUserIdentity observes current-token uniqueness, account state,
// all membership anomalies and at most two active joined candidates together.
// A signed tenant restricts membership evidence to that tenant; an empty signed
// tenant examines the full native set. No owner/personal/role fallback is used.
func (*UserTenantDAO) ObserveNativeUserIdentity(ctx context.Context, db *gorm.DB, accessToken, signedTenantID string) (NativeUserIdentityObservation, error) {
	var rows []struct {
		UserCount           int64
		UserID              *string
		UserStatus          *string
		IsActive            *string
		IsSuperuser         *bool
		UnknownState        int64
		DuplicateMembership int64
		OrphanMembership    int64
		TenantID            *string
	}
	// Keep the entire observation in one SQL statement: separate default
	// transactions or repeated DB getters do not establish candidate completeness.
	// The LIMIT in token_users only distinguishes zero/one/multiple users. The
	// candidate LIMIT follows full-set anomaly aggregation and never hides it.
	query := metadataSQL(db, `WITH token_users AS (
 SELECT id, status, is_active, is_superuser FROM `+metadataIdentifier(db, "user")+` WHERE access_token = ? LIMIT 2
), membership_rows AS (
 SELECT m.id, m.tenant_id, m.status AS membership_status,
        t.id AS tenant_row_id, t.status AS tenant_status,
        COUNT(*) OVER (PARTITION BY m.user_id, m.tenant_id) AS pair_count
 FROM token_users u JOIN user_tenant m ON m.user_id = u.id
 LEFT JOIN tenant t ON t.id = m.tenant_id
 WHERE (? = '' OR m.tenant_id = ?)
), evidence AS (
 SELECT COALESCE(MAX(CASE WHEN id IS NULL OR id = '' OR tenant_id IS NULL OR tenant_id = ''
   OR membership_status IS NULL OR membership_status NOT IN ('0','1','2','3','4','5')
   OR (membership_status = '1' AND tenant_row_id IS NOT NULL
       AND (tenant_status IS NULL OR tenant_status NOT IN ('0','1')))
   THEN 1 ELSE 0 END), 0) AS unknown_state,
   COALESCE(MAX(CASE WHEN pair_count > 1 THEN 1 ELSE 0 END), 0) AS duplicate_membership,
   COALESCE(MAX(CASE WHEN membership_status = '1' AND tenant_row_id IS NULL THEN 1 ELSE 0 END), 0) AS orphan_membership
 FROM membership_rows
), candidates AS (
 SELECT tenant_id FROM membership_rows
 WHERE membership_status = '1' AND tenant_status = '1' AND tenant_row_id IS NOT NULL
 LIMIT 2
)
SELECT n.user_count, u.id AS user_id, u.status AS user_status, u.is_active, u.is_superuser,
       e.unknown_state, e.duplicate_membership, e.orphan_membership, c.tenant_id
 FROM (SELECT COUNT(*) AS user_count FROM token_users) n
 LEFT JOIN token_users u ON n.user_count = 1
 CROSS JOIN evidence e
 LEFT JOIN candidates c ON 1 = 1`)
	err := db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Discard}).Raw(query, accessToken, signedTenantID, signedTenantID).Scan(&rows).Error
	if err != nil {
		return NativeUserIdentityObservation{}, err
	}
	if len(rows) == 0 {
		return NativeUserIdentityObservation{}, gorm.ErrInvalidData
	}
	first := rows[0]
	observation := NativeUserIdentityObservation{
		UserCount:           first.UserCount,
		UnknownState:        first.UnknownState != 0,
		DuplicateMembership: first.DuplicateMembership != 0,
		OrphanMembership:    first.OrphanMembership != 0,
	}
	if first.UserID != nil {
		observation.User = &entity.User{ID: *first.UserID, Status: first.UserStatus, IsSuperuser: first.IsSuperuser}
		if first.IsActive != nil {
			observation.User.IsActive = *first.IsActive
		}
	}
	for _, row := range rows {
		if row.TenantID != nil {
			observation.TenantIDs = append(observation.TenantIDs, *row.TenantID)
		}
	}
	return observation, nil
}
