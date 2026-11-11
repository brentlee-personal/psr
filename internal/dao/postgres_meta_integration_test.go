//go:build integration

//
//  Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.
//

package dao

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"ragflow/internal/entity"
	"ragflow/internal/server/config"
	"strings"
	"testing"
	"time"
)

// This test writes only a newly named schema in an explicitly approved test
// database. It retains that schema for independent inspection; it never drops
// an existing database, schema, table or snapshot.
func TestPostgreSQLMetadataIntegration(t *testing.T) {
	dsn := os.Getenv("PSR_POSTGRES_TEST_DSN")
	expected := os.Getenv("PSR_POSTGRES_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PSR_POSTGRES_TEST_DSN is not configured")
	}
	if os.Getenv("PSR_POSTGRES_TEST_ALLOW_WRITE") != "1" || !strings.HasPrefix(expected, "psr_go_") {
		t.Fatal("explicit isolated PostgreSQL test target required")
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		var err error
		dsn, err = pq.ParseURL(dsn)
		if err != nil {
			t.Fatal("invalid PostgreSQL URL")
		}
	}
	probe, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal("open test target")
	}
	defer probe.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var actual, version string
	if err = probe.QueryRowContext(ctx, "SELECT current_database(),current_setting('server_version')").Scan(&actual, &version); err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatal("PostgreSQL database identity mismatch")
	}
	schemaName := fmt.Sprintf("psr_pg_%d", time.Now().UnixNano())
	if _, err = probe.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatal(err)
	}
	t.Logf("database=%s PostgreSQL=%s isolated_schema=%s", actual, version, schemaName)
	dialector := newPostgreSQLDialector(config.PostgreSQLConfig{}).(postgresMetaDialector)
	dialector.Config.DSN = dsn + " search_path=" + quoteMetaDSNValue(schemaName)
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), TranslateError: true})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pool.SetMaxOpenConns(3)
	t.Run("initialize_and_repeat_migrations", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			if err := migrateMetadataSchema(ctx, db, true); err != nil {
				t.Fatalf("migration pass %d: %v", i+1, err)
			}
		}
	})
	if t.Failed() {
		return
	}
	t.Run("user_json_permission_rows_and_rollback", func(t *testing.T) {
		status := "1"
		u := &entity.User{ID: "pg-test-user", Nickname: "PG Test", Email: "pg-test@example.invalid", Status: &status}
		if err := db.WithContext(ctx).Create(u).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.WithContext(ctx).Create(&entity.UserTenant{ID: "pg-test-membership", UserID: u.ID, TenantID: "pg-test-tenant", Role: "normal", Status: &status}).Error; err != nil {
			t.Fatal(err)
		}
		rows, err := NewUserTenantDAO().GetMembersByTenantID(ctx, db, "pg-test-tenant")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Email != u.Email {
			t.Fatal("user join did not preserve membership")
		}
		marker := errors.New("rollback fixture")
		err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&entity.User{ID: "pg-rolled-back", Nickname: "Rollback", Email: "rollback@example.invalid"}).Error; err != nil {
				return err
			}
			return marker
		})
		if !errors.Is(err, marker) {
			t.Fatal(err)
		}
		var n int64
		if err := db.Model(&entity.User{}).Where("id = ?", "pg-rolled-back").Count(&n).Error; err != nil || n != 0 {
			t.Fatal("transaction rollback did not remove fixture")
		}
	})
	t.Run("case_insensitive_dataset_unique_constraint", func(t *testing.T) {
		status := "1"
		if err := db.WithContext(ctx).Create(&entity.Knowledgebase{ID: "pg-kb-1", TenantID: "pg-test-tenant", Name: "Retail", Status: &status}).Error; err != nil {
			t.Fatal(err)
		}
		err := db.WithContext(ctx).Create(&entity.Knowledgebase{ID: "pg-kb-2", TenantID: "pg-test-tenant", Name: "retail", Status: &status}).Error
		if !errors.Is(err, gorm.ErrDuplicatedKey) {
			t.Fatalf("case-insensitive unique constraint returned %v", err)
		}
	})
	t.Run("limited_delete_and_generated_ids", func(t *testing.T) {
		for _, s := range []string{"one", "two", "three"} {
			r := &entity.TimeRecord{Data: s}
			if err := NewTimeRecordDAO().Create(ctx, db, r); err != nil {
				t.Fatal(err)
			}
			if r.ID == 0 {
				t.Fatal("PostgreSQL RETURNING did not populate id")
			}
		}
		if err := NewTimeRecordDAO().DeleteOldest(ctx, db, 1); err != nil {
			t.Fatal(err)
		}
		n, err := NewTimeRecordDAO().GetCount(ctx, db)
		if err != nil || n != 2 {
			t.Fatal("limited deletion count did not match")
		}
	})
	t.Run("non_migrating_startup", func(t *testing.T) {
		if err := migrateMetadataSchema(ctx, db, false); err != nil {
			t.Fatal(err)
		}
	})
}
