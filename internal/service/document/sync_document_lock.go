package document

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"time"

	"ragflow/internal/common"
	"ragflow/internal/dao"
	"ragflow/internal/service"
)

// acquireSyncDocumentLock serializes sync side effects across database sessions,
// including for documents that have not been inserted yet. SQLite uses the
// process lock only for the default in-memory unit fixtures.
func acquireSyncDocumentLock(ctx context.Context, docID string) (func(), error) {
	key := "sync-document:" + service.Hash128(docID)
	switch dao.DB.Dialector.Name() {
	case "sqlite":
		// SQLite is used by the in-memory service unit fixtures. Its lock key
		// must differ from the parse lock acquired by StartParseDocuments.
		return lockDocumentParse(key), nil
	case "postgres":
		pool, err := dao.OpenIndependentPostgresDB(dao.DB)
		if err != nil {
			return nil, err
		}
		unlock, err := acquirePostgresSyncDocumentLock(ctx, pool, key)
		if err != nil {
			_ = pool.Close()
			return nil, err
		}
		return func() {
			defer pool.Close()
			unlock()
		}, nil
	default:
		pool, err := dao.OpenIndependentMySQLDB(dao.DB)
		if err != nil {
			return nil, err
		}
		unlock, err := acquireMySQLSyncDocumentLock(ctx, pool, key)
		if err != nil {
			_ = pool.Close()
			return nil, err
		}
		return func() {
			// Closing the dedicated session also releases a named lock if the
			// explicit release fails; it is never returned to the application pool.
			defer pool.Close()
			unlock()
		}, nil
	}
}

func syncDocumentLockID(key string) int64 {
	sum := sha256.Sum256([]byte(key))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

// acquirePostgresSyncDocumentLock owns a session advisory lock on one dedicated
// PostgreSQL connection until its release callback runs.
func acquirePostgresSyncDocumentLock(ctx context.Context, pool *sql.DB, key string) (func(), error) {
	lockCtx, cancel := context.WithTimeout(ctx, cleanupBatchTimeout)
	defer cancel()
	conn, err := pool.Conn(lockCtx)
	if err != nil {
		return nil, err
	}
	lockID := syncDocumentLockID(key)
	if _, err := conn.ExecContext(lockCtx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return func() {
		defer conn.Close()
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupBatchTimeout)
		defer cancel()
		var released sql.NullBool
		if err := conn.QueryRowContext(releaseCtx, "SELECT pg_advisory_unlock($1)", lockID).Scan(&released); err != nil || !released.Valid || !released.Bool {
			common.Warn(fmt.Sprintf("release postgres sync document lock %s (%d) failed: %v", key, lockID, err))
		}
	}, nil
}

// acquireMySQLSyncDocumentLock owns a named lock on one dedicated connection
// until its release callback runs. It holds no row lock or SQL transaction.
func acquireMySQLSyncDocumentLock(ctx context.Context, pool *sql.DB, key string) (func(), error) {
	lockCtx, cancel := context.WithTimeout(ctx, cleanupBatchTimeout)
	defer cancel()
	conn, err := pool.Conn(lockCtx)
	if err != nil {
		return nil, err
	}
	var granted sql.NullInt64
	err = conn.QueryRowContext(lockCtx, "SELECT GET_LOCK(?, ?)", key, int64(cleanupBatchTimeout/time.Second)).Scan(&granted)
	if err != nil || !granted.Valid || granted.Int64 != 1 {
		_ = conn.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("could not acquire sync document lock %s", key)
	}
	return func() {
		defer conn.Close()
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupBatchTimeout)
		defer cancel()
		var released sql.NullInt64
		if err := conn.QueryRowContext(releaseCtx, "SELECT RELEASE_LOCK(?)", key).Scan(&released); err != nil || !released.Valid || released.Int64 != 1 {
			common.Warn(fmt.Sprintf("release sync document lock %s failed: %v", key, err))
		}
	}, nil
}
