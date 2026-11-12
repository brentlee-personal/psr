package sqlresult

import "context"

// Query issues exactly one QueryContext, with no retry or statement rewriting.
// A driver ignoring cancellation is waited for synchronously.
func Query(ctx context.Context, q Queryer, statement string, args []any, limits Limits) (ReadResult, error) {
	if limits.MaxRows <= 0 || limits.MaxBytes <= 0 {
		return ReadResult{}, ErrLimits
	}
	return query(ctx, q, statement, args, readOptions{limits: limits})
}

// QueryLegacy is exclusively for ExeSQL's existing model-tool contract: raw
// errors, no byte/cell validation, ignored close errors and no completeness claim.
// Restricted execution must use Query instead.
func QueryLegacy(ctx context.Context, q Queryer, statement string, maxRows int) (ReadResult, error) {
	return query(ctx, q, statement, nil, readOptions{limits: Limits{MaxRows: maxRows}, legacy: true})
}

func query(ctx context.Context, q Queryer, statement string, args []any, options readOptions) (ReadResult, error) {
	if !options.legacy {
		if err := contextError(ctx); err != nil {
			return ReadResult{}, err
		}
	}
	rows, err := q.QueryContext(ctx, statement, args...)
	if err != nil {
		if options.legacy {
			return ReadResult{}, err
		}
		if err := contextError(ctx); err != nil {
			return ReadResult{}, err
		}
		return ReadResult{}, ErrQuery
	}
	return read(ctx, rows, options)
}

func contextError(ctx context.Context) error {
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return ErrDeadline
	case context.Canceled:
		return ErrCanceled
	default:
		return nil
	}
}
