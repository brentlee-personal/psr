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
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
	"ragflow/internal/server/config"
	"strings"
	"testing"
)

func openPostgreSQLMetaForSQL(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(newPostgreSQLDialector(config.PostgreSQLConfig{Host: "postgres.internal", Port: 5432, User: "psr", DatabaseName: "psr_go_test"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	return db
}
func TestPostgreSQLMetadataNativeTypes(t *testing.T) {
	db := openPostgreSQLMetaForSQL(t)
	for _, model := range autoMigrateModels() {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			t.Fatal(err)
		}
		for _, field := range stmt.Schema.Fields {
			got := db.Dialector.DataTypeOf(field)
			for _, bad := range []string{"longtext", "datetime", "tinyint"} {
				if strings.Contains(strings.ToLower(got), bad) {
					t.Errorf("%s.%s emits non-PostgreSQL type %s", stmt.Schema.Table, field.DBName, got)
				}
			}
		}
	}
	if got := db.Dialector.DataTypeOf(&schema.Field{DataType: "longtext"}); got != "text" {
		t.Fatal(got)
	}
}
func TestPostgreSQLMetadataQuotesOnlyIdentifiers(t *testing.T) {
	db := openPostgreSQLMetaForSQL(t)
	pairs := map[string]string{
		"SELECT user.id FROM user WHERE nickname='user.id'": "SELECT \"user\".id FROM \"user\" WHERE nickname='user.id'",
		"JOIN user ON user.id=user_tenant.user_id":          "JOIN \"user\" ON \"user\".id=user_tenant.user_id",
		"-- user.id\nSELECT user.email":                     "-- user.id\nSELECT \"user\".email",
		"/* user.id */ user.email":                          "/* user.id */ \"user\".email",
	}
	for in, want := range pairs {
		if got := metadataSQL(db, in); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}
func TestPostgreSQLMetadataRemoteTLSDefault(t *testing.T) {
	d := newPostgreSQLDialector(config.PostgreSQLConfig{Host: "postgres.internal", Port: 5432, User: "u", DatabaseName: "d", Password: "x'y"}).(postgresMetaDialector)
	if !strings.Contains(d.Config.DSN, "sslmode='verify-full'") || !strings.Contains(d.Config.DSN, "password='x\\'y'") {
		t.Fatal("DSN does not preserve remote TLS and quoted values")
	}
	if isVastbaseMetaDB(openPostgreSQLMetaForSQL(t)) {
		t.Fatal("native PostgreSQL must not match Vastbase")
	}
}
