package sqlresult

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// All fixtures are synthetic drivers. No test observes a real DB terminal state.
type fixture struct {
	columns                                        []string
	values                                         [][]driver.Value
	queryErr, nextErr, closeErr                    error
	onQuery, onNext, onClose                       func()
	queryCount, nextCount, closeCount, columnCount atomic.Int32
	scanMismatch                                   bool
	metadataMismatch                               bool
	receivedStatement                              string
	receivedArgs                                   []driver.NamedValue
	receivedContext                                context.Context
	moreSets                                       bool
	position                                       int
	reuse                                          []byte
}

type connector struct{ f *fixture }

func (c connector) Connect(context.Context) (driver.Conn, error) { return &conn{c.f}, nil }
func (c connector) Driver() driver.Driver                        { return syntheticDriver{} }

type syntheticDriver struct{}

func (syntheticDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type conn struct{ f *fixture }

func (*conn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected prepare") }
func (*conn) Begin() (driver.Tx, error)           { return nil, errors.New("unexpected transaction") }
func (*conn) Close() error                        { return nil }
func (c *conn) QueryContext(ctx context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
	c.f.queryCount.Add(1)
	c.f.receivedStatement, c.f.receivedContext = statement, ctx
	c.f.receivedArgs = append([]driver.NamedValue(nil), args...)
	if c.f.onQuery != nil {
		c.f.onQuery()
	}
	if c.f.queryErr != nil {
		return nil, c.f.queryErr
	}
	return &syntheticRows{c.f}, nil
}

type syntheticRows struct{ f *fixture }

func (r *syntheticRows) Columns() []string {
	n := r.f.columnCount.Add(1)
	if (r.f.scanMismatch && n >= 3) || (r.f.metadataMismatch && n >= 2) {
		return []string{"x", "extra"}
	}
	return r.f.columns
}
func (r *syntheticRows) ColumnTypeDatabaseTypeName(int) string { return "SYNTHETIC" }
func (r *syntheticRows) Close() error {
	r.f.closeCount.Add(1)
	if r.f.onClose != nil {
		r.f.onClose()
	}
	return r.f.closeErr
}
func (r *syntheticRows) Next(dest []driver.Value) error {
	r.f.nextCount.Add(1)
	if r.f.onNext != nil {
		r.f.onNext()
	}
	if r.f.position == len(r.f.values) {
		if r.f.nextErr != nil {
			return r.f.nextErr
		}
		return io.EOF
	}
	copy(dest, r.f.values[r.f.position])
	if r.f.reuse != nil {
		copy(r.f.reuse, dest[0].(string))
		dest[0] = r.f.reuse
	}
	r.f.position++
	return nil
}
func (r *syntheticRows) HasNextResultSet() bool { return r.f.moreSets }
func (r *syntheticRows) NextResultSet() error {
	if r.f.moreSets {
		r.f.moreSets = false
		return nil
	}
	return io.EOF
}

func database(t *testing.T, f *fixture) *sql.DB {
	t.Helper()
	db := sql.OpenDB(connector{f})
	t.Cleanup(func() { _ = db.Close() })
	return db
}
func limits() Limits { return Limits{MaxRows: 100, MaxBytes: 1 << 20} }
func run(t *testing.T, f *fixture, lim Limits) (ReadResult, error) {
	t.Helper()
	return Query(t.Context(), database(t, f), "synthetic statement", nil, lim)
}

func TestRowCompletenessBoundaries(t *testing.T) {
	for _, n := range []int{0, 99, 100, 101} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f := &fixture{columns: []string{"x"}}
			for i := range n {
				f.values = append(f.values, []driver.Value{int64(i)})
			}
			r, err := run(t, f, limits())
			if n <= 100 {
				if err != nil || r.Completeness != Complete || len(r.Rows) != n {
					t.Fatalf("complete=%v err=%v rows=%d", r.Completeness, err, len(r.Rows))
				}
			} else if err != ErrRowLimit || r.Completeness != Exceeded || len(r.Rows) != 100 {
				t.Fatalf("limit=%v err=%v rows=%d", r.Completeness, err, len(r.Rows))
			}
			if r.RowsClose != CloseConfirmed || f.closeCount.Load() != 1 {
				t.Fatal("Rows.Close not confirmed exactly once")
			}
			if f.queryCount.Load() != 1 {
				t.Fatal("query retried")
			}
			if len(r.Columns) != 1 {
				t.Fatal("empty result lost schema")
			}
		})
	}
}

func TestOrderedCellsPreservePrecisionAndOwnBytes(t *testing.T) {
	ts := time.Date(2026, 11, 11, 13, 45, 12, 987654321, time.FixedZone("fixture", 8*3600))
	f := &fixture{columns: []string{"duplicate", "duplicate", "time", "integer", "nilbytes"}, reuse: make([]byte, 3), values: [][]driver.Value{
		{"one", "first", ts, int64(9007199254740993), []byte(nil)},
		{"two", "second", ts, int64(9007199254740994), []byte{}},
	}}
	r, err := run(t, f, limits())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Columns) != 5 || r.Columns[0].Name != r.Columns[1].Name || r.Columns[0].DatabaseType != "SYNTHETIC" {
		t.Fatal("columns changed")
	}
	if string(r.Rows[0][0].([]byte)) != "one" || string(r.Rows[1][0].([]byte)) != "two" {
		t.Fatal("driver reused buffer mutated retained row")
	}
	if r.Rows[0][2].(time.Time) != ts || r.Rows[0][3].(int64) != 9007199254740993 {
		t.Fatal("exact values changed")
	}
	if r.Rows[0][4].([]byte) != nil || r.Rows[1][4].([]byte) == nil {
		t.Fatal("nil and empty bytes conflated")
	}
}

func TestCellsRejectedWithoutCoercion(t *testing.T) {
	for name, cell := range map[string]driver.Value{"nan": math.NaN(), "inf": math.Inf(1), "minusinf": math.Inf(-1), "unsupported": struct{ Secret string }{"CANARY_RESULT"}, "invalidtime": time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)} {
		t.Run(name, func(t *testing.T) {
			r, err := run(t, &fixture{columns: []string{"x"}, values: [][]driver.Value{{cell}}}, limits())
			if err != ErrCell || r.Completeness == Complete || len(r.Rows) != 0 {
				t.Fatalf("error=%v complete=%v retained=%d", err, r.Completeness, len(r.Rows))
			}
		})
	}
}

func TestPayloadByteBoundaryIncludesColumnsAndJSON(t *testing.T) {
	newFixture := func() *fixture {
		return &fixture{columns: []string{"x"}, values: [][]driver.Value{{[]byte("\x00<&\"CANARY_RESULT")}}}
	}
	r, err := run(t, newFixture(), limits())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(struct {
		Columns []Column `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}{r.Columns, r.Rows})
	if r.RetainedBytes != len(encoded) {
		t.Fatal("byte definition differs")
	}
	for _, delta := range []int{-1, 0, 1} {
		t.Run(fmt.Sprint(delta), func(t *testing.T) {
			got, err := run(t, newFixture(), Limits{100, len(encoded) + delta})
			if delta < 0 {
				if err != ErrByteLimit || got.Completeness != Exceeded || len(got.Rows) != 0 {
					t.Fatal("oversize accepted")
				}
				if cap(got.Rows) > 0 && got.Rows[:cap(got.Rows)][0] != nil {
					t.Fatal("oversized row retained in backing array")
				}
			} else if err != nil || got.Completeness != Complete {
				t.Fatal("exact/smaller payload rejected")
			}
		})
	}
	t.Run("metadata alone", func(t *testing.T) {
		r, err := run(t, &fixture{columns: []string{strings.Repeat("x", 100)}}, Limits{100, 10})
		if err != ErrByteLimit || r.Completeness != Exceeded || r.Columns != nil {
			t.Fatal("metadata not bounded")
		}
	})
}

func TestErrorsNeverClaimComplete(t *testing.T) {
	canary := errors.New("CANARY_DRIVER result CANARY_RESULT sql CANARY_SQL")
	cases := []struct {
		name string
		f    *fixture
		lim  Limits
		want error
	}{
		{"query", &fixture{queryErr: canary}, limits(), ErrQuery},
		{"zero columns", &fixture{}, limits(), ErrMetadata},
		{"metadata mismatch", &fixture{columns: []string{"x"}, metadataMismatch: true}, limits(), ErrMetadata},
		{"next", &fixture{columns: []string{"x"}, nextErr: canary}, limits(), ErrRows},
		{"scan", &fixture{columns: []string{"x"}, values: [][]driver.Value{{int64(1)}}, scanMismatch: true}, limits(), ErrScan},
		{"EOF close", &fixture{columns: []string{"x"}, closeErr: canary}, limits(), ErrRows},
		{"explicit close", &fixture{columns: []string{"x"}, values: [][]driver.Value{{int64(1)}, {int64(2)}}, closeErr: canary}, Limits{1, 10000}, ErrRowLimit},
		{"next result set", &fixture{columns: []string{"x"}, moreSets: true}, limits(), ErrResultSets},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := run(t, c.f, c.lim)
			if err != c.want || r.Completeness == Complete {
				t.Fatalf("err=%v want=%v completeness=%v", err, c.want, r.Completeness)
			}
			if c.f.closeErr != nil && r.RowsClose != CloseUnknown {
				t.Fatal("close failure confirmed")
			}
			if c.f.queryCount.Load() != 1 {
				t.Fatal("retried")
			}
			encoded, _ := json.Marshal(err)
			if strings.Contains(fmt.Sprint(err)+string(encoded), "CANARY") {
				t.Fatal("driver error leaked")
			}
		})
	}
}

func TestReadInvalidLimitsStillOwnsClose(t *testing.T) {
	f := &fixture{columns: []string{"x"}}
	rows, err := database(t, f).QueryContext(t.Context(), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Read(t.Context(), rows, Limits{})
	if err != ErrLimits || f.closeCount.Load() != 1 || r.Completeness != Unknown {
		t.Fatal("invalid read did not close")
	}
	_, err = Query(t.Context(), database(t, f), "synthetic", nil, Limits{})
	if err != ErrLimits || f.queryCount.Load() != 1 {
		t.Fatal("invalid query touched database")
	}
}

func TestQueryPassesCallerContextStatementAndArgumentsOnce(t *testing.T) {
	f := &fixture{columns: []string{"x"}}
	ctx := t.Context()
	r, err := Query(ctx, database(t, f), "SELECT exact($1, $2)", []any{int64(42), "binding"}, limits())
	if err != nil || r.Completeness != Complete || f.queryCount.Load() != 1 {
		t.Fatal("single query failed")
	}
	if f.receivedContext != ctx || f.receivedStatement != "SELECT exact($1, $2)" || len(f.receivedArgs) != 2 || f.receivedArgs[0].Value != int64(42) || f.receivedArgs[1].Value != "binding" {
		t.Fatal("query context, text or arguments changed")
	}
}

func TestCancellationStages(t *testing.T) {
	for _, stage := range []string{"before query", "query", "next", "close"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f := &fixture{columns: []string{"x"}, values: [][]driver.Value{{int64(1)}}}
			switch stage {
			case "before query":
				cancel()
			case "query":
				f.onQuery = cancel
			case "next":
				f.onNext = cancel
			case "close":
				f.onClose = cancel
			}
			r, err := Query(ctx, database(t, f), "synthetic", nil, limits())
			if err != ErrCanceled || r.Completeness != Unknown {
				t.Fatalf("err=%v completeness=%v", err, r.Completeness)
			}
			if stage == "before query" && f.queryCount.Load() != 0 {
				t.Fatal("queried canceled request")
			}
			if stage != "before query" && f.closeCount.Load() != 1 {
				t.Fatal("returned before close")
			}
		})
	}
}

func TestExpiredContextZeroQueries(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	f := &fixture{}
	r, err := Query(ctx, database(t, f), "synthetic", nil, limits())
	if err != ErrDeadline || f.queryCount.Load() != 0 || r.Completeness != Unknown {
		t.Fatal("expired request executed")
	}
}

type cancellationAtCheck struct {
	context.Context
	cancel context.CancelFunc
	checks int
	at     int
}

func (c *cancellationAtCheck) Err() error {
	c.checks++
	if c.checks == c.at {
		c.cancel()
	}
	return c.Context.Err()
}

func TestCancellationImmediatelyAfterScan(t *testing.T) {
	// Rows use an independent live driver context. This synthetic reader context
	// cancels precisely at Read's post-Scan check, avoiding scheduler races and
	// making no assertion about real database cancellation.
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &cancellationAtCheck{Context: base, cancel: cancel, at: 6}
	f := &fixture{columns: []string{"x"}, values: [][]driver.Value{{"CANARY_RESULT"}}}
	rows, err := database(t, f).QueryContext(t.Context(), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Read(ctx, rows, limits())
	if err != ErrCanceled || r.Completeness != Unknown || len(r.Rows) != 0 || f.closeCount.Load() != 1 || f.nextCount.Load() != 1 {
		t.Fatal("post-Scan cancellation released a row or skipped close")
	}
}

func TestCancellationDuringFailedClose(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := &fixture{columns: []string{"x"}, closeErr: errors.New("CANARY_CLOSE"), onClose: cancel}
	r, err := Query(ctx, database(t, f), "synthetic", nil, limits())
	if err != ErrCanceled || r.Completeness != Unknown || r.RowsClose != CloseUnknown {
		t.Fatal("cancel/close-error race claimed completion or cleanup")
	}
}

func TestCancellationWaitsForIgnoringDriver(t *testing.T) {
	for _, stage := range []string{"query", "next", "close"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var enteredOnce atomic.Bool
			block := func() {
				if enteredOnce.CompareAndSwap(false, true) {
					close(entered)
					<-release
				}
			}
			f := &fixture{columns: []string{"x"}, onClose: func() { close(closed) }}
			switch stage {
			case "query":
				f.onQuery = block
			case "next":
				f.onNext = block
			case "close":
				f.onClose = func() { block(); close(closed) }
			}
			db := database(t, f)
			done := make(chan error, 1)
			go func() { _, err := Query(ctx, db, "synthetic", nil, limits()); done <- err }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("driver not entered")
			}
			cancel()
			select {
			case <-done:
				t.Fatal("reader returned while driver still running")
			case <-time.After(20 * time.Millisecond):
			}
			close(release)
			select {
			case err := <-done:
				if err != ErrCanceled {
					t.Fatalf("err=%v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("reader failed to return")
			}
			select {
			case <-closed:
			default:
				t.Fatal("return preceded driver close")
			}
		})
	}
}

func TestNoLoggingAndSafeErrorJSON(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	f := &fixture{queryErr: errors.New("CANARY_SQL CANARY_RESULT")}
	_, err := Query(t.Context(), database(t, f), "CANARY_SQL", nil, limits())
	encoded, _ := json.Marshal(err)
	if buf.Len() != 0 || strings.Contains(string(encoded), "CANARY") || strings.Contains(fmt.Sprint(err), "CANARY") {
		t.Fatal("sensitive output escaped")
	}
}

func TestLegacyReaderContract(t *testing.T) {
	f := &fixture{columns: []string{"duplicate", "duplicate"}, values: [][]driver.Value{{math.NaN(), []byte("one")}, {math.Inf(1), []byte("two")}}, closeErr: errors.New("ignored close")}
	r, err := QueryLegacy(t.Context(), database(t, f), "synthetic", 1)
	if err != nil || len(r.Rows) != 1 || !math.IsNaN(r.Rows[0][0].(float64)) || r.Completeness != Unknown || r.RowsClose != CloseUnknown || f.nextCount.Load() != 2 {
		t.Fatalf("legacy changed err=%v", err)
	}
	raw := errors.New("original driver text")
	_, err = QueryLegacy(t.Context(), database(t, &fixture{queryErr: raw}), "synthetic", 1)
	if err != raw {
		t.Fatal("legacy raw error changed")
	}
}
