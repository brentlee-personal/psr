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

package vastbase

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"ragflow/internal/engine/types"
	"ragflow/internal/server/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestBuildPGVectorDSN(t *testing.T) {
	t.Setenv("PGVECTOR_DSN", "")
	got := buildPGVectorDSN(config.PGVectorConfig{
		Host:     "pg.example.internal",
		Port:     5432,
		User:     "worker user",
		Password: "p@ss word'",
		DBName:   "psr_db",
		SSLMode:  "disable",
	})
	want := `host='pg.example.internal' port=5432 user='worker user' dbname='psr_db' sslmode='disable' ` +
		`password='p@ss word\'' connect_timeout=30 statement_timeout=300000`
	if got != want {
		t.Fatalf("DSN = %q, want %q", got, want)
	}
	if red := redactDSN(got); !strings.Contains(red, "password=***") || strings.Contains(red, "p@ss") {
		t.Fatalf("redacted DSN leaked secret: %q", red)
	}

	if remoteDefault := buildPGVectorDSN(config.PGVectorConfig{Host: "pg.example.internal"}); !strings.Contains(remoteDefault, "sslmode='verify-full'") {
		t.Fatalf("remote default sslmode must be verify-full, got %q", remoteDefault)
	}
	if localDefault := buildPGVectorDSN(config.PGVectorConfig{Host: "127.0.0.1"}); !strings.Contains(localDefault, "sslmode='disable'") {
		t.Fatalf("local default sslmode must be disable, got %q", localDefault)
	}

	t.Setenv("PGVECTOR_DSN", "postgres://u:secret@h:5432/db")
	if override := buildPGVectorDSN(config.PGVectorConfig{Host: "ignored"}); override != "postgres://u:secret@h:5432/db" {
		t.Fatalf("PGVECTOR_DSN override = %q", override)
	}
}

func TestInitializePGVectorChecksExtensionsAndFallback(t *testing.T) {
	// 1. Vector extension present + jiebacfg installed
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	e := newPGVectorEngineWithDB(config.PGVectorConfig{DBName: "psr", TSConfig: "jiebacfg"}, db)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT version()")).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow("PostgreSQL 16.14"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT extversion FROM pg_extension WHERE extname = 'vector'")).
		WillReturnRows(sqlmock.NewRows([]string{"extversion"}).AddRow("0.8.0.2"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM pg_ts_config WHERE cfgname = $1")).
		WithArgs("jiebacfg").WillReturnRows(sqlmock.NewRows([]string{"one"}).AddRow(1))
	if err := e.initializePGVector(context.Background()); err != nil {
		t.Fatalf("initializePGVector(jiebacfg): %v", err)
	}
	if e.tsConfigName() != "jiebacfg" {
		t.Fatalf("tsConfigName = %q, want jiebacfg", e.tsConfigName())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// 2. Default jiebacfg missing -> degrades to simple
	db2, mock2, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	e2 := newPGVectorEngineWithDB(config.PGVectorConfig{DBName: "psr"}, db2)
	mock2.ExpectQuery(regexp.QuoteMeta("SELECT version()")).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow("PostgreSQL 16.14"))
	mock2.ExpectQuery(regexp.QuoteMeta("SELECT extversion FROM pg_extension WHERE extname = 'vector'")).
		WillReturnRows(sqlmock.NewRows([]string{"extversion"}).AddRow("0.8.0.2"))
	mock2.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM pg_ts_config WHERE cfgname = $1")).
		WithArgs("jiebacfg").WillReturnError(sql.ErrNoRows)
	if err := e2.initializePGVector(context.Background()); err != nil {
		t.Fatalf("initializePGVector fallback: %v", err)
	}
	if e2.tsConfigName() != "simple" {
		t.Fatalf("tsConfigName = %q, want simple", e2.tsConfigName())
	}
	if err := mock2.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	_ = db2.Close()

	// 3. Vector extension missing -> returns error
	db3, mock3, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	e3 := newPGVectorEngineWithDB(config.PGVectorConfig{DBName: "psr"}, db3)
	mock3.ExpectQuery(regexp.QuoteMeta("SELECT version()")).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow("PostgreSQL 16.14"))
	mock3.ExpectQuery(regexp.QuoteMeta("SELECT extversion FROM pg_extension WHERE extname = 'vector'")).
		WillReturnError(sql.ErrNoRows)
	if err := e3.initializePGVector(context.Background()); err == nil || !strings.Contains(err.Error(), "vector") {
		t.Fatalf("expected missing vector extension error, got %v", err)
	}
	_ = db3.Close()
}

func TestPGVectorDDLUsesVectorAndHalfvecHNSWAndTSConfig(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e := newPGVectorEngineWithDB(config.PGVectorConfig{TSConfig: "jiebacfg"}, db)

	if got := e.dynamicColumnForField("q_1024_vec"); got.typeSQL != "vector(1024)" {
		t.Fatalf("1024 dynamic type = %q, want vector(1024)", got.typeSQL)
	}
	if got := e.dynamicColumnForField("q_3072_vec"); got.typeSQL != "halfvec(3072)" {
		t.Fatalf("3072 dynamic type = %q, want halfvec(3072)", got.typeSQL)
	}

	// 1024-dim HNSW index
	mock.ExpectQuery(regexp.QuoteMeta(columnCountQuery())).WithArgs("t1", "q_1024_vec").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta(`ALTER TABLE "t1" ADD COLUMN "q_1024_vec" vector(1024)`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectCount(mock, columnCountQuery(), "t1", "q_1024_vec")
	expectIndexProvision(mock, "t1", regularIndexName("t1", "q_1024_vec_hnsw"),
		`CREATE INDEX IF NOT EXISTS "ix_t1_q_1024_vec_hnsw" ON "t1" USING hnsw ("q_1024_vec" vector_cosine_ops) WITH (m=16, ef_construction=64)`)
	if err := e.ensureVectorColumnAndIndex(context.Background(), "t1", 1024); err != nil {
		t.Fatal(err)
	}

	// 3072-dim halfvec HNSW index
	mock.ExpectQuery(regexp.QuoteMeta(columnCountQuery())).WithArgs("t1", "q_3072_vec").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta(`ALTER TABLE "t1" ADD COLUMN "q_3072_vec" halfvec(3072)`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectCount(mock, columnCountQuery(), "t1", "q_3072_vec")
	expectIndexProvision(mock, "t1", regularIndexName("t1", "q_3072_vec_hnsw"),
		`CREATE INDEX IF NOT EXISTS "ix_t1_q_3072_vec_hnsw" ON "t1" USING hnsw ("q_3072_vec" halfvec_cosine_ops) WITH (m=16, ef_construction=64)`)
	if err := e.ensureVectorColumnAndIndex(context.Background(), "t1", 3072); err != nil {
		t.Fatal(err)
	}

	// Full-text GIN index using jiebacfg
	expressions := make([]string, 0, len(fullTextFields))
	for _, field := range fullTextFields {
		expressions = append(expressions, fmt.Sprintf("to_tsvector('jiebacfg', %s)", quoteIdent(field)))
	}
	mock.ExpectQuery(regexp.QuoteMeta(indexCountQuery())).WithArgs("t1", "text_gin_idx_t1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta(fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS "text_gin_idx_t1" ON "t1" USING gin (%s)`,
		strings.Join(expressions, ", ")))).WillReturnResult(sqlmock.NewResult(0, 0))
	if err := e.ensureFullTextIndexes(context.Background(), "t1", fullTextFields); err != nil {
		t.Fatal(err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPGVectorSearchQueries(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e := newPGVectorEngineWithDB(config.PGVectorConfig{TSConfig: "jiebacfg"}, db)

	// 1. vectorCTE guards NaN and keeps inner ORDER BY bare
	dense := &types.MatchDenseExpr{
		VectorColumnName:  "q_1024_vec",
		EmbeddingData:     []float64{1, 0, 0},
		EmbeddingDataType: "float",
		TopN:              10,
	}
	args := &sqlArgs{}
	vecSQL, err := e.vectorCTE("t1", []string{"id"}, `"kb_id" = $1`, []interface{}{"kb-1"}, dense, 10, 0.2, "[1,0,0]", args)
	if err != nil {
		t.Fatal(err)
	}
	wantVec := `SELECT * FROM (SELECT "id", COALESCE(NULLIF(1 - ("q_1024_vec" <=> $2), 'NaN'::double precision), 0.0::double precision) AS _score ` +
		`FROM "t1" WHERE "kb_id" = $1 ORDER BY "q_1024_vec" <=> $3 LIMIT 10) AS knn WHERE _score >= $4`
	if vecSQL != wantVec {
		t.Fatalf("vectorCTE =\n%s\nwant\n%s", vecSQL, wantVec)
	}
	if !reflect.DeepEqual(args.values, []interface{}{"kb-1", "[1,0,0]", "[1,0,0]", 0.2}) {
		t.Fatalf("vectorCTE args = %#v", args.values)
	}

	// 2. pgFullTextCTE queries jiebacfg lexemes and uses jiebacfg in to_tsvector/plainto_tsquery
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT to_tsvector('jiebacfg', $1)::text AS lexemes`)).
		WithArgs("促销 规则").
		WillReturnRows(sqlmock.NewRows([]string{"lexemes"}).AddRow("'促销':1 '规则':2"))
	ftArgs := &sqlArgs{}
	ftSQL, err := e.pgFullTextCTE(context.Background(), "t1", "id", `"id"`, []string{"content_ltks"}, []string{"1"},
		`"kb_id" = $1`, []interface{}{"kb-1"}, "促销 规则", "0%", 20, ftArgs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ftSQL, "to_tsvector('jiebacfg', \"content_ltks\") @@ plainto_tsquery('jiebacfg', $2)") {
		t.Fatalf("pgFullTextCTE missing jiebacfg predicate:\n%s", ftSQL)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPGVectorMemoryFusionPlaceholderAlignment(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e := newPGVectorEngineWithDB(config.PGVectorConfig{TSConfig: "jiebacfg"}, db)

	expectCount(mock, tableCountQuery(), "memory_t1")
	rows := sqlmock.NewRows([]string{"column_name", "data_type", "column_default", "is_nullable"})
	for _, column := range []string{"id", "memory_id", "content_ltks", "forget_at", "q_2_vec"} {
		rows.AddRow(column, "text", nil, "YES")
	}
	mock.ExpectQuery(regexp.QuoteMeta(listColumnsQuery)).WithArgs("memory_t1").WillReturnRows(rows)

	vecSim := `COALESCE(NULLIF(1 - ("q_2_vec" <=> $4), 'NaN'::double precision), 0.0::double precision)`
	score := fmt.Sprintf(`(ts_rank(to_tsvector('simple', COALESCE(tokenized_content_ltks, '')), to_tsquery('simple', $3)) * 0.7 + %s * 0.3)`, vecSim)
	inner := `SELECT "content_ltks", "id", "q_2_vec", ` + score + ` AS _score FROM "memory_t1" ` +
		`WHERE "memory_id" IN ($1) AND ("forget_at" IS NULL OR "forget_at" = '') ` +
		`AND to_tsvector('simple', COALESCE(tokenized_content_ltks, '')) @@ to_tsquery('simple', $2) LIMIT 20`
	outerVec := `COALESCE(NULLIF(1 - ("q_2_vec" <=> $5), 'NaN'::double precision), 0.0::double precision)`
	query := `SELECT * FROM (` + inner + `) AS candidates WHERE ` + outerVec + ` >= $6 ORDER BY _score DESC LIMIT 10 OFFSET 0`
	mock.ExpectQuery(regexp.QuoteMeta(query)).
		WithArgs("mem-1", "hello", "hello", "[0.5,0.25]", "[0.5,0.25]", 0.2).
		WillReturnRows(sqlmock.NewRows([]string{"content_ltks", "id", "q_2_vec", "_score"}).
			AddRow("hello", "m1_1", "[0.5,0.25]", 0.6))

	result, err := e.Search(context.Background(), &types.SearchRequest{
		IndexNames:   []string{"memory_t1"},
		KbIDs:        []string{"mem-1"},
		SelectFields: []string{"content"},
		Limit:        10,
		MatchExprs: []interface{}{
			&types.MatchTextExpr{MatchingText: "hello", TopN: 10},
			&types.MatchDenseExpr{VectorColumnName: "q_2_vec", EmbeddingData: []float64{0.5, 0.25},
				TopN: 10, ExtraOptions: map[string]interface{}{"similarity": 0.2}},
			&types.FusionExpr{Method: "weighted_sum", TopN: 10,
				FusionParams: map[string]interface{}{"weights": "0.7,0.3"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Chunks) != 1 || result.Chunks[0]["content_embed"] == nil {
		t.Fatalf("chunks = %#v", result.Chunks)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
