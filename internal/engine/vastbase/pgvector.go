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
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"ragflow/internal/common"
	"ragflow/internal/server/config"

	"go.uber.org/zap"
)

const (
	defaultPGVectorTSConfig   = "jiebacfg"
	fallbackPGVectorTSConfig  = "simple"
	maxPGVectorHNSWDim        = 2000
	maxPGVectorHalfvecHNSWDim = 4000
	defaultPGVectorMaxConns   = 16
	defaultPGVectorMaxIdle    = 4
)

var tsConfigPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// NewPGVectorEngine constructs a standard PostgreSQL + pgvector DocEngine
// sharing the PG-compatible schema, CRUD, filter, metadata, and hybrid search
// implementation.
func NewPGVectorEngine(cfg config.PGVectorConfig) (*Engine, error) {
	dsn := buildPGVectorDSN(cfg)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("pgvector: open: %w", err)
	}
	maxOpen := cfg.MaxConnections
	if maxOpen <= 0 {
		maxOpen = defaultPGVectorMaxConns
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = defaultPGVectorMaxIdle
	}
	if maxIdle > maxOpen {
		maxIdle = maxOpen
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(connMaxLifetime)

	e := &Engine{
		db:              db,
		dbName:          cfg.DBName,
		engineType:      "pgvector",
		dbCompatibility: compatPG,
		pgvector:        true,
		tsConfig:        strings.TrimSpace(cfg.TSConfig),
		dsnSafe:         redactDSN(dsn),
	}

	if lastErr := pingWithRetry(db, "PGVector", e.dsnSafe); lastErr != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pgvector: connect %s: %w", e.dsnSafe, lastErr)
	}

	initCtx, cancelInit := context.WithTimeout(context.Background(), initializeTimeout)
	defer cancelInit()
	if err := e.initializePGVector(initCtx); err != nil {
		_ = db.Close()
		return nil, err
	}
	common.Info("PGVector engine initialized",
		zap.String("dsn", e.dsnSafe),
		zap.String("ts_config", e.tsConfigName()))
	return e, nil
}

func newPGVectorEngineWithDB(cfg config.PGVectorConfig, db *sql.DB) *Engine {
	tsCfg := strings.TrimSpace(cfg.TSConfig)
	if tsCfg == "" {
		tsCfg = defaultPGVectorTSConfig
	}
	return &Engine{
		db:              db,
		dbName:          cfg.DBName,
		engineType:      "pgvector",
		dbCompatibility: compatPG,
		pgvector:        true,
		tsConfig:        tsCfg,
		dsnSafe:         "redacted",
	}
}

func buildPGVectorDSN(cfg config.PGVectorConfig) string {
	if env := os.Getenv("PGVECTOR_DSN"); env != "" {
		return env
	}
	host, port, user, password, dbName := "localhost", 5432, "ragflow", "", "rag_flow"
	sslMode := ""
	if cfg.Host != "" {
		host = cfg.Host
	}
	if cfg.Port != 0 {
		port = cfg.Port
	}
	if cfg.User != "" {
		user = cfg.User
	}
	password = cfg.Password
	if cfg.DBName != "" {
		dbName = cfg.DBName
	}
	if cfg.SSLMode != "" {
		sslMode = cfg.SSLMode
	}
	if sslMode == "" {
		if isLocalDBHost(host) {
			sslMode = "disable"
		} else {
			sslMode = "verify-full"
		}
	}
	parts := []string{
		fmt.Sprintf("host=%s", quoteDSNValue(host)),
		fmt.Sprintf("port=%d", port),
		fmt.Sprintf("user=%s", quoteDSNValue(user)),
		fmt.Sprintf("dbname=%s", quoteDSNValue(dbName)),
		fmt.Sprintf("sslmode=%s", quoteDSNValue(sslMode)),
	}
	if password != "" {
		parts = append(parts, fmt.Sprintf("password=%s", quoteDSNValue(password)))
	}
	if cfg.SSLRootCert != "" {
		parts = append(parts, fmt.Sprintf("sslrootcert=%s", quoteDSNValue(cfg.SSLRootCert)))
	}
	parts = append(parts,
		"connect_timeout=30",
		"statement_timeout=300000",
	)
	return strings.Join(parts, " ")
}

func (e *Engine) initializePGVector(ctx context.Context) error {
	var version string
	if err := e.db.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil {
		return fmt.Errorf("pgvector: get version: %w", err)
	}
	common.Info("PGVector server version", zap.String("version", version))

	var extVersion string
	if err := e.db.QueryRowContext(ctx,
		"SELECT extversion FROM pg_extension WHERE extname = 'vector'").Scan(&extVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("pgvector: extension \"vector\" is not installed in database %q", e.dbName)
		}
		return fmt.Errorf("pgvector: check vector extension: %w", err)
	}
	common.Info("pgvector extension ready", zap.String("extversion", extVersion))

	configured := strings.TrimSpace(e.tsConfig)
	target := configured
	if target == "" {
		target = defaultPGVectorTSConfig
	}
	if !tsConfigPattern.MatchString(target) {
		return fmt.Errorf("pgvector: invalid text search configuration %q", target)
	}

	var exists int
	err := e.db.QueryRowContext(ctx, "SELECT 1 FROM pg_ts_config WHERE cfgname = $1", target).Scan(&exists)
	switch {
	case err == nil:
		e.tsConfig = target
	case errors.Is(err, sql.ErrNoRows) && (configured == "" || target == defaultPGVectorTSConfig):
		common.Warn("pgvector text search configuration not found; falling back to simple",
			zap.String("configured", target),
			zap.String("fallback", fallbackPGVectorTSConfig))
		e.tsConfig = fallbackPGVectorTSConfig
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("pgvector: text search configuration %q does not exist", target)
	default:
		return fmt.Errorf("pgvector: check text search configuration %q: %w", target, err)
	}
	return nil
}

func pgvectorColumnType(vectorSize int) string {
	if vectorSize > maxPGVectorHNSWDim && vectorSize <= maxPGVectorHalfvecHNSWDim {
		return fmt.Sprintf("halfvec(%d)", vectorSize)
	}
	return fmt.Sprintf("vector(%d)", vectorSize)
}

func pgvectorHNSWOps(vectorSize int) (string, bool) {
	switch {
	case vectorSize <= 0:
		return "", false
	case vectorSize <= maxPGVectorHNSWDim:
		return "vector_cosine_ops", true
	case vectorSize <= maxPGVectorHalfvecHNSWDim:
		return "halfvec_cosine_ops", true
	default:
		return "", false
	}
}
