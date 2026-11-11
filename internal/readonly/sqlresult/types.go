// Package sqlresult reads database/sql results without selecting a data source,
// authorizing a query, or reporting database execution termination.
package sqlresult

import (
	"context"
	"database/sql"
)

// Queryer is satisfied by *sql.DB, *sql.Conn and *sql.Tx. The caller owns it.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Limits bounds retained rows and their JSON payload (columns and rows only).
// The caller must separately bound the final response envelope and the provider.
type Limits struct {
	MaxRows  int
	MaxBytes int
}

type Completeness uint8

const (
	Unknown Completeness = iota
	Complete
	Exceeded
)

type CloseState uint8

const (
	CloseUnknown CloseState = iota
	CloseConfirmed
)

type Column struct {
	Name         string `json:"name"`
	DatabaseType string `json:"database_type,omitempty"`
}

// ReadResult describes only this reader stage. RowsClose says nothing about
// transaction rollback, connection isolation, or database query termination.
type ReadResult struct {
	Columns       []Column     `json:"columns"`
	Rows          [][]any      `json:"rows"`
	Completeness  Completeness `json:"completeness"`
	RowsClose     CloseState   `json:"rows_close"`
	RetainedBytes int          `json:"retained_bytes"`
}

// Error is a safe category. It never wraps a driver error, SQL or cell value.
type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrLimits     Error = "sqlresult: invalid limits"
	ErrQuery      Error = "sqlresult: query failed"
	ErrMetadata   Error = "sqlresult: invalid result metadata"
	ErrScan       Error = "sqlresult: scan failed"
	ErrRows       Error = "sqlresult: iteration failed"
	ErrClose      Error = "sqlresult: close failed"
	ErrCanceled   Error = "sqlresult: canceled"
	ErrDeadline   Error = "sqlresult: deadline exceeded"
	ErrRowLimit   Error = "sqlresult: row limit exceeded"
	ErrByteLimit  Error = "sqlresult: byte limit exceeded"
	ErrCell       Error = "sqlresult: unsupported cell"
	ErrResultSets Error = "sqlresult: additional result set"
)
