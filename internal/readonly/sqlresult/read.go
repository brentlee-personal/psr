package sqlresult

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"time"
)

type readOptions struct {
	limits Limits
	legacy bool
}

// Read takes ownership of Rows.Close, including on invalid limits or cancellation.
// It requires EOF, no Rows.Err, confirmed close and a live context for Complete.
func Read(ctx context.Context, rows *sql.Rows, limits Limits) (ReadResult, error) {
	return read(ctx, rows, readOptions{limits: limits})
}

func read(ctx context.Context, rows *sql.Rows, options readOptions) (result ReadResult, err error) {
	if rows == nil {
		return result, ErrMetadata
	}
	defer func() {
		closeErr := rows.Close()
		if options.legacy {
			return
		}
		if closeErr == nil && rows.Err() == nil {
			result.RowsClose = CloseConfirmed
		}
		if closeErr != nil {
			result.Completeness = Unknown
			if err == nil {
				err = ErrClose
			}
		}
		if ctxErr := contextError(ctx); ctxErr != nil {
			result.Completeness = Unknown
			err = ctxErr
		}
		if err != nil && result.Completeness == Complete {
			result.Completeness = Unknown
		}
	}()
	if !options.legacy {
		if options.limits.MaxRows <= 0 || options.limits.MaxBytes <= 0 {
			return result, ErrLimits
		}
		if err = contextError(ctx); err != nil {
			return result, err
		}
	}
	names, colsErr := rows.Columns()
	if colsErr != nil {
		if options.legacy {
			return result, colsErr
		}
		return result, ErrMetadata
	}
	result.Columns = make([]Column, len(names))
	for i, name := range names {
		result.Columns[i].Name = name
	}
	result.Rows = make([][]any, 0)
	if !options.legacy {
		if err = contextError(ctx); err != nil {
			return result, err
		}
		if len(names) == 0 {
			return result, ErrMetadata
		}
		types, typesErr := rows.ColumnTypes()
		if typesErr != nil {
			return result, ErrMetadata
		}
		for i, typ := range types {
			result.Columns[i].DatabaseType = typ.DatabaseTypeName()
		}
		if err = contextError(ctx); err != nil {
			return result, err
		}
		if err = measure(&result, options.limits.MaxBytes); err != nil {
			// Do not retain oversized schema metadata in an error result.
			result.Columns = nil
			result.Rows = nil
			return result, err
		}
	}
	for {
		if !options.legacy {
			if err = contextError(ctx); err != nil {
				return result, err
			}
		}
		next := rows.Next()
		if !options.legacy {
			if err = contextError(ctx); err != nil {
				return result, err
			}
		}
		if !next {
			break
		}
		// Probe the extra row without scanning or retaining it.
		if len(result.Rows) >= options.limits.MaxRows {
			if options.legacy {
				break
			}
			result.Completeness = Exceeded
			return result, ErrRowLimit
		}
		raw := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if scanErr := rows.Scan(ptrs...); scanErr != nil {
			if options.legacy {
				return result, scanErr
			}
			return result, ErrScan
		}
		if !options.legacy {
			if err = contextError(ctx); err != nil {
				return result, err
			}
		}
		for i, cell := range raw {
			if b, ok := cell.([]byte); ok {
				if b != nil {
					raw[i] = append([]byte{}, b...)
				}
			}
			if !options.legacy && !validCell(raw[i]) {
				return result, ErrCell
			}
		}
		result.Rows = append(result.Rows, raw)
		if !options.legacy {
			if err = measure(&result, options.limits.MaxBytes); err != nil {
				// Clear the backing array reference before shortening the slice.
				result.Rows[len(result.Rows)-1] = nil
				result.Rows = result.Rows[:len(result.Rows)-1]
				return result, err
			}
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		if options.legacy {
			return result, rowsErr
		}
		return result, ErrRows
	}
	if !options.legacy {
		if rows.NextResultSet() {
			return result, ErrResultSets
		}
		if rows.Err() != nil {
			return result, ErrRows
		}
		if err = contextError(ctx); err != nil {
			return result, err
		}
		result.Completeness = Complete
	}
	return result, nil
}

func validCell(cell any) bool {
	switch v := cell.(type) {
	case nil, bool, int64, string, []byte, time.Time:
		return true
	case float64:
		return !math.IsNaN(v) && !math.IsInf(v, 0)
	default:
		return false
	}
}

func measure(result *ReadResult, limit int) error {
	// This is the precise retained payload definition, not a wire-memory bound.
	payload := struct {
		Columns []Column `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}{result.Columns, result.Rows}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ErrCell
	}
	if len(encoded) > limit {
		result.Completeness = Exceeded
		return ErrByteLimit
	}
	result.RetainedBytes = len(encoded)
	return nil
}
