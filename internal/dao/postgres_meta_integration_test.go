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
	"encoding/json"
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
	dialector.Config.DSN = dsn + " timezone='UTC' search_path=" + quoteMetaDSNValue(schemaName)
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
	t.Run("user_membership_date_and_rollback", func(t *testing.T) {
		status := "1"
		stamp := time.Date(2026, 11, 11, 12, 34, 56, 0, time.UTC)
		u := &entity.User{ID: "pg-test-user", Nickname: "PG Test", Email: "pg-test@example.invalid", Status: &status, BaseModel: entity.BaseModel{UpdateDate: &stamp}}
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
		if len(rows) != 1 || rows[0].Email != u.Email || rows[0].UpdateDate != "2026-11-11T12:34:56" {
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
	t.Run("json_roundtrip_regex_and_catalog", func(t *testing.T) {
		tenant, title := "pg-test-tenant", "JSON fixture"
		expectedJSON := entity.JSONMap{"enabled": true, "count": 17, "nested": map[string]interface{}{"id": "9007199254740993"}, "list": []interface{}{"a", float64(2)}}
		canvas := &entity.UserCanvas{ID: "pg-json-canvas", UserID: "pg-test-user", TenantID: &tenant, Title: &title, Tags: "finance, go", DSL: expectedJSON}
		if err := db.WithContext(ctx).Create(canvas).Error; err != nil {
			t.Fatal(err)
		}
		var loaded entity.UserCanvas
		if err := db.WithContext(ctx).Where("id = ?", canvas.ID).Take(&loaded).Error; err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(expectedJSON)
		got, _ := json.Marshal(loaded.DSL)
		if string(got) != string(want) {
			t.Fatal("JSON content did not roundtrip")
		}
		var matched []entity.UserCanvas
		q := applyUserCanvasTagFilter(ctx, db, db.Model(&entity.UserCanvas{}).Where("id = ?", canvas.ID), []string{"go"})
		if err := q.Find(&matched).Error; err != nil || len(matched) != 1 {
			t.Fatalf("native regex exact tag match: %v rows=%d", err, len(matched))
		}
		var absent []entity.UserCanvas
		if err := applyUserCanvasTagFilter(ctx, db, db.Model(&entity.UserCanvas{}).Where("id = ?", canvas.ID), []string{"g"}).Find(&absent).Error; err != nil || len(absent) != 0 {
			t.Fatalf("regex substring must not match whole tag: %v rows=%d", err, len(absent))
		}
		if ok, err := columnExists(ctx, db, "user_canvas", "dsl"); err != nil || !ok {
			t.Fatal("PostgreSQL column catalog lookup failed")
		}
		if kind, _, ok, err := columnDefinition(ctx, db, "user_canvas", "dsl"); err != nil || !ok || kind != "text" {
			t.Fatalf("JSON-backed text column type=%s ok=%t error=%v", kind, ok, err)
		}
		if auto, err := isAutoIncrementColumn(ctx, db, "time_records", "id"); err != nil || !auto {
			t.Fatal("PostgreSQL generated id catalog lookup failed")
		}
		if unique, err := hasUniqueIndex(ctx, db, "user", []string{"email"}); err != nil || !unique {
			t.Fatal("native unique index catalog lookup failed")
		}
		if err := db.WithContext(ctx).Exec("CREATE TABLE pg_index_fixture (a text,b text)").Error; err != nil {
			t.Fatal(err)
		}
		if err := addUniqueIndex(ctx, db, "pg_index_fixture", "pg_index_fixture_a_b", []string{"a", "b"}); err != nil {
			t.Fatal(err)
		}
		if unique, err := hasUniqueIndex(ctx, db, "pg_index_fixture", []string{"a"}); err != nil || unique {
			t.Fatal("broader index must not satisfy single-column uniqueness")
		}
		if unique, err := hasUniqueIndex(ctx, db, "pg_index_fixture", []string{"a", "b"}); err != nil || !unique {
			t.Fatal("composite unique index not recognized")
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
	t.Run("canvas_title_casefold_constraint_and_concurrency", func(t *testing.T) {
		insert := func(id, user, category, title string) error {
			return db.WithContext(ctx).Create(&entity.UserCanvas{ID: id, UserID: user, CanvasCategory: category, Title: &title, DSL: entity.JSONMap{}}).Error
		}
		if err := insert("pg-title-one", "pg-title-owner", "agent_canvas", "Retail"); err != nil {
			t.Fatal(err)
		}
		if err := insert("pg-title-two", "pg-title-owner", "agent_canvas", "retail"); !errors.Is(err, gorm.ErrDuplicatedKey) {
			t.Fatalf("casefold title duplicate was not rejected: %v", err)
		}
		if err := insert("pg-title-other-owner", "pg-title-other", "agent_canvas", "retail"); err != nil {
			t.Fatal("casefold index widened across owners")
		}
		if err := insert("pg-title-other-category", "pg-title-owner", "another_category", "retail"); err != nil {
			t.Fatal("casefold index widened across categories")
		}
		results := make(chan error, 2)
		start := make(chan struct{})
		for i, title := range []string{"Concurrent", "concurrent"} {
			go func(i int, title string) {
				<-start
				results <- insert(fmt.Sprintf("pg-title-race-%d", i), "pg-title-race-owner", "agent_canvas", title)
			}(i, title)
		}
		close(start)
		passed, duplicate := 0, 0
		for i := 0; i < 2; i++ {
			err := <-results
			if err == nil {
				passed++
			} else if errors.Is(err, gorm.ErrDuplicatedKey) {
				duplicate++
			} else {
				t.Fatal(err)
			}
		}
		if passed != 1 || duplicate != 1 {
			t.Fatalf("concurrent casefold constraint: passed=%d duplicate=%d", passed, duplicate)
		}
	})
	t.Run("native_json_legacy_normalization_preserves_content", func(t *testing.T) {
		status := "1"
		original := entity.JSONMap{"unchanged": "value", "nested": map[string]interface{}{"count": float64(7)}}
		k := &entity.Knowledgebase{ID: "pg-json-kb", TenantID: "pg-test-tenant", Name: "JSON migration", Status: &status, ParserConfig: original}
		if err := db.Create(k).Error; err != nil {
			t.Fatal(err)
		}
		if err := normalizeModelIDJSONColumn(ctx, db, "knowledgebase", "parser_config"); err != nil {
			t.Fatalf("native json comparison in legacy normalization: %v", err)
		}
		var got entity.Knowledgebase
		if err := db.Where("id = ?", k.ID).Take(&got).Error; err != nil {
			t.Fatal(err)
		}
		before, _ := json.Marshal(original)
		after, _ := json.Marshal(got.ParserConfig)
		if string(before) != string(after) {
			t.Fatal("unchanged JSON was rewritten")
		}
	})
	t.Run("reject_incompatible_existing_constraint_shapes", func(t *testing.T) {
		openFixture := func(suffix string) *gorm.DB {
			ns := schemaName + suffix
			if _, err := probe.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(ns)); err != nil {
				t.Fatal(err)
			}
			driver := newPostgreSQLDialector(config.PostgreSQLConfig{}).(postgresMetaDialector)
			driver.Config.DSN = dsn + " search_path=" + quoteMetaDSNValue(ns)
			fixture, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			pool, err := fixture.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { pool.Close() })
			return fixture
		}
		badColumn := openFixture("_column")
		if err := badColumn.Exec("CREATE TABLE knowledgebase(id text,tenant_id text,name text,status text,name_ci text)").Error; err != nil {
			t.Fatal(err)
		}
		if err := badColumn.Exec("INSERT INTO knowledgebase VALUES ('one','tenant','Retail','1',NULL),('two','tenant','retail','1',NULL)").Error; err != nil {
			t.Fatal(err)
		}
		err := postgresKnowledgebaseNameUnique(ctx, badColumn)
		if err == nil || !strings.Contains(err.Error(), "incompatible generated column") {
			t.Fatalf("plain name_ci column was not rejected: %v", err)
		}
		var count int64
		if err := badColumn.Table("knowledgebase").Count(&count).Error; err != nil || count != 2 {
			t.Fatal("constraint rejection changed existing rows")
		}
		badIndex := openFixture("_index")
		if err := badIndex.Exec(`CREATE TABLE knowledgebase(id text,tenant_id text,name text,status text,
   name_ci varchar(128) GENERATED ALWAYS AS (CASE WHEN status='1' THEN lower(name) ELSE NULL END) STORED)`).Error; err != nil {
			t.Fatal(err)
		}
		if err := badIndex.Exec("CREATE INDEX idx_kb_tenant_name_ci ON knowledgebase(id)").Error; err != nil {
			t.Fatal(err)
		}
		err = postgresKnowledgebaseNameUnique(ctx, badIndex)
		if err == nil || !strings.Contains(err.Error(), "unexpected shape") {
			t.Fatalf("wrong KB index was not rejected: %v", err)
		}
		if err := badIndex.Exec("CREATE TABLE user_canvas(user_id text,canvas_category text,title text)").Error; err != nil {
			t.Fatal(err)
		}
		if err := badIndex.Exec("CREATE INDEX idx_user_canvas_user_category_title ON user_canvas(title)").Error; err != nil {
			t.Fatal(err)
		}
		err = migrateUserCanvasTitleUnique(ctx, badIndex)
		if err == nil || !strings.Contains(err.Error(), "unexpected shape") {
			t.Fatalf("wrong canvas index was not rejected: %v", err)
		}
	})
	t.Run("non_migrating_startup", func(t *testing.T) {
		if err := migrateMetadataSchema(ctx, db, false); err != nil {
			t.Fatal(err)
		}
	})
}
