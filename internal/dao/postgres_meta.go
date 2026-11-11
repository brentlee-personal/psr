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
	"fmt"
	"strings"
	"unicode"

	"github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
	"ragflow/internal/server/config"
)

// postgresMetaDialector keeps native PostgreSQL grammar distinct from Vastbase
// B mode while mapping the shared entities' storage annotations to native types.
type postgresMetaDialector struct{ *postgres.Dialector }
type postgresMetaMigrator struct{ postgres.Migrator }

func (d postgresMetaDialector) DataTypeOf(field *schema.Field) string {
	switch strings.ToLower(string(field.DataType)) {
	case "longtext", "mediumtext", "tinytext":
		return "text"
	case "datetime":
		return "timestamptz"
	case "tinyint":
		return "smallint"
	case "blob", "longblob", "mediumblob", "tinyblob":
		return "bytea"
	}
	return d.Dialector.DataTypeOf(field)
}
func (d postgresMetaDialector) Migrator(db *gorm.DB) gorm.Migrator {
	m := d.Dialector.Migrator(db).(postgres.Migrator)
	return postgresMetaMigrator{Migrator: m}
}
func (m postgresMetaMigrator) MigrateColumnUnique(dst interface{}, field *schema.Field, columnType gorm.ColumnType) error {
	if phantomUniqueDrop(field, columnType) {
		return nil
	}
	return m.Migrator.MigrateColumnUnique(dst, field, columnType)
}
func newPostgreSQLDialector(cfg config.PostgreSQLConfig) gorm.Dialector {
	sslMode := cfg.SSLMode
	if sslMode == "" {
		if isLocalMetaDBHost(cfg.Host) {
			sslMode = "disable"
		} else {
			sslMode = "verify-full"
		}
	}
	dsn := fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=%s connect_timeout=5 statement_timeout=300000",
		quoteMetaDSNValue(cfg.Host), cfg.Port, quoteMetaDSNValue(cfg.User), quoteMetaDSNValue(cfg.DatabaseName), quoteMetaDSNValue(sslMode))
	if cfg.Password != "" {
		dsn += " password=" + quoteMetaDSNValue(cfg.Password)
	}
	if cfg.SSLRootCert != "" {
		dsn += " sslrootcert=" + quoteMetaDSNValue(cfg.SSLRootCert)
	}
	return postgresMetaDialector{Dialector: postgres.New(postgres.Config{DSN: dsn}).(*postgres.Dialector)}
}
func isPostgreSQLMetaDB(db *gorm.DB) bool {
	return db.Dialector.Name() == "postgres" && !isVastbaseMetaDB(db)
}
func metadataIdentifier(db *gorm.DB, name string) string {
	var out strings.Builder
	db.Dialector.QuoteTo(&out, name)
	return out.String()
}

// metadataSQL quotes the reserved user table in handwritten SQL fragments.
// String literals, comments and already quoted identifiers remain untouched.
func metadataSQL(db *gorm.DB, text string) string {
	if !isPostgreSQLMetaDB(db) {
		return text
	}
	var out strings.Builder
	for i := 0; i < len(text); {
		start := i
		if text[i] == '\'' || text[i] == '"' || text[i] == '`' {
			quote := text[i]
			i++
			for i < len(text) {
				if text[i] == quote {
					i++
					if i < len(text) && text[i] == quote {
						i++
						continue
					}
					break
				}
				if text[i] == '\\' && i+1 < len(text) {
					i += 2
				} else {
					i++
				}
			}
			part := text[start:i]
			if quote == '`' {
				part = "\"" + strings.ReplaceAll(part[1:len(part)-1], "\"", "\"\"") + "\""
			}
			out.WriteString(part)
			continue
		}
		if i+1 < len(text) && text[i:i+2] == "--" {
			for i < len(text) && text[i] != '\n' {
				i++
			}
			out.WriteString(text[start:i])
			continue
		}
		if i+1 < len(text) && text[i:i+2] == "/*" {
			i += 2
			for i+1 < len(text) && text[i:i+2] != "*/" {
				i++
			}
			if i+1 < len(text) {
				i += 2
			}
			out.WriteString(text[start:i])
			continue
		}
		if unicode.IsLetter(rune(text[i])) || text[i] == '_' {
			i++
			for i < len(text) && (unicode.IsLetter(rune(text[i])) || unicode.IsDigit(rune(text[i])) || text[i] == '_') {
				i++
			}
			word := text[start:i]
			if strings.EqualFold(word, "user") {
				out.WriteString("\"user\"")
			} else {
				out.WriteString(word)
			}
			continue
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String()
}

func postgresKnowledgebaseNameUnique(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).Exec(`ALTER TABLE knowledgebase ADD COLUMN IF NOT EXISTS name_ci varchar(128)
 GENERATED ALWAYS AS (CASE WHEN status = '1' THEN lower(name) ELSE NULL END) STORED`).Error; err != nil {
		return err
	}
	return db.WithContext(ctx).Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_kb_tenant_name_ci ON knowledgebase (tenant_id,name_ci)`).Error
}

func postgresHasUniqueIndex(ctx context.Context, db *gorm.DB, table string, columns []string) (bool, error) {
	var count int64
	err := db.WithContext(ctx).Raw(`SELECT count(*) FROM (
  SELECT i.indexrelid FROM pg_index i JOIN pg_class t ON t.oid=i.indrelid
  JOIN pg_namespace n ON n.oid=t.relnamespace
  JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum,seq) ON k.seq<=i.indnkeyatts
  JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=k.attnum
  WHERE n.nspname=current_schema() AND t.relname=? AND i.indisunique AND NOT i.indisprimary
    AND i.indpred IS NULL AND i.indexprs IS NULL AND i.indisvalid
  GROUP BY i.indexrelid HAVING count(*)=? AND count(*) FILTER (WHERE a.attname=ANY(?::text[]))=?
 ) matches`, table, len(columns), pq.Array(columns), len(columns)).Scan(&count).Error
	return count > 0, err
}
func metadataColumnDDL(db *gorm.DB, ddl string) string {
	if isPostgreSQLMetaDB(db) {
		return strings.NewReplacer(" tinyint ", " smallint ", " TINYINT ", " smallint ", " longtext ", " text ", " LONGTEXT ", " text ", " datetime ", " timestamptz ", " DATETIME ", " timestamptz ").Replace(ddl)
	}
	return ddl
}

// Manual migrations run before AutoMigrate to preserve legacy inputs. A new
// PostgreSQL schema also needs its constraints after tables are first created.
func ensurePostgreSQLMetadataConstraints(ctx context.Context, db *gorm.DB) error {
	if err := addUniqueIndex(ctx, db, "tenant_llm", "uk_tenant_llm", []string{"tenant_id", "llm_factory", "llm_name"}); err != nil {
		return err
	}

	for _, migration := range []func(context.Context, *gorm.DB) error{migrateAddUniqueEmail, migrateIngestionTaskDocumentIDUnique, migrateKnowledgebaseNameUnique, migrateUserCanvasTitleUnique} {
		if err := migration(ctx, db); err != nil {
			return err
		}
	}
	return nil
}
