package readonly

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"ragflow/internal/readonly/resultdigest"
	"ragflow/internal/readonly/sqlresult"
	"ragflow/internal/readonly/starrocksguard"
)

func starrocksConnection(path string) (string, string, error) {
	values := map[string]string{}
	for _, key := range []string{"host", "port", "database", "user", "password", "sslmode"} {
		b, err := protected(filepath.Join(path, key))
		if err != nil || (len(b) == 0 && key != "database") {
			return "", "", ErrUnverified
		}
		values[key] = string(b)
	}
	port, err := strconv.Atoi(values["port"])
	if err != nil || port < 1 || port > 65535 || strings.ContainsAny(values["host"], "/\x00\r\n") {
		return "", "", ErrUnverified
	}
	config := mysql.NewConfig()
	config.Net = "tcp"
	config.Addr = net.JoinHostPort(values["host"], values["port"])
	config.User = values["user"]
	config.Passwd = values["password"]
	config.DBName = "" // Physical references are fully qualified; never USE an implicit internal catalog.
	config.Timeout = 5 * time.Second
	config.ReadTimeout = 35 * time.Second
	config.WriteTimeout = 5 * time.Second
	config.MultiStatements = false
	config.AllowAllFiles = false
	config.ParseTime = true
	switch values["sslmode"] {
	case "disable":
		config.TLSConfig = "false"
	case "require", "verify-full":
		config.TLSConfig = "true"
	default:
		return "", "", ErrUnverified
	}
	raw, _ := json.Marshal(values)
	return config.FormatDSN(), hash(raw), nil
}

func validateStarRocks(p Policy) error {
	sr := p.StarRocks
	if sr != nil && sr.AsOf != "" {
		if _, err := time.Parse("2006-01-02", sr.AsOf); err != nil {
			return ErrUnverified
		}
	}
	if sr == nil || p.Dialect != "starrocks" {
		return ErrUnverified
	}
	for _, v := range []string{sr.CatalogSHA256, sr.MappingSHA256, sr.DefinitionSHA256, sr.CurrentUserSHA256} {
		if !digest.MatchString(v) {
			return ErrUnverified
		}
	}
	if starrocksguard.Validate(starrocksguard.Policy{Relations: sr.Relations, Functions: sr.Functions}) != nil {
		return ErrUnverified
	}
	return nil
}

// Each independent connection disables inherited roles, bounds execution and
// disables unbounded SELECTs. No session setting is accepted from question SQL.
func prepareStarRocks(ctx context.Context, conn *sql.Conn, seconds int) error {
	if seconds < 1 || seconds > 30 {
		return ErrUnverified
	}
	for _, statement := range []string{"SET ROLE NONE", fmt.Sprintf("SET query_timeout = %d", seconds), "SET sql_select_limit = 101"} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return ErrUnverified
		}
	}
	var role sql.NullString
	var mode string
	if conn.QueryRowContext(ctx, "SELECT CURRENT_ROLE(),@@sql_mode").Scan(&role, &mode) != nil || (role.Valid && role.String != "" && !strings.EqualFold(role.String, "NONE")) || strings.Contains(strings.ToUpper(mode), "NO_BACKSLASH_ESCAPES") {
		return ErrUnverified
	}
	return nil
}

func catalogIdentifier(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '_' && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}
func physicalName(s string) (string, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return "", ErrUnverified
	}
	for i, v := range parts {
		if !catalogIdentifier(v) {
			return "", ErrUnverified
		}
		parts[i] = "`" + v + "`"
	}
	return strings.Join(parts, "."), nil
}
func cellText(v any) string {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

// Fresh runtime checks use DESCRIBE and account grants, never cached fixture
// schema or information inferred from tenant labels. Raw grants never escape.
func (s *Source) starrocksAuthority(ctx context.Context, a Authority) (Authority, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Authority{}, ErrUnverified
	}
	defer conn.Close()
	if prepareStarRocks(ctx, conn, 10) != nil {
		return Authority{}, ErrUnverified
	}
	var user string
	if conn.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&user) != nil || hash([]byte(user)) != a.Policy.StarRocks.CurrentUserSHA256 {
		return Authority{}, ErrUnverified
	}
	grants, err := conn.QueryContext(ctx, "SHOW GRANTS")
	if err != nil {
		return Authority{}, ErrUnverified
	}
	statements := []string{}
	for grants.Next() {
		fields, err := grants.Columns()
		if err != nil {
			grants.Close()
			return Authority{}, ErrUnverified
		}
		values := make([]any, len(fields))
		targets := make([]any, len(fields))
		for i := range values {
			targets[i] = &values[i]
		}
		if grants.Scan(targets...) != nil {
			grants.Close()
			return Authority{}, ErrUnverified
		}
		for _, value := range values {
			statements = append(statements, cellText(value))
		}
	}
	// Grant display is provenance, not a global account privilege gate.
	// Disabled administrative roles and grants on unrelated metadata do not
	// enlarge model SQL: NONE plus AST/scope enforcement and LIMIT 0 apply.
	_ = statements
	if grants.Err() != nil || grants.Close() != nil {
		return Authority{}, ErrUnverified
	}
	for _, relation := range a.Policy.StarRocks.Relations {
		physical, err := physicalName(relation.Physical)
		if err != nil {
			return Authority{}, err
		}
		rows, err := conn.QueryContext(ctx, "DESCRIBE "+physical)
		if err != nil {
			return Authority{}, ErrUnverified
		}
		names, err := rows.Columns()
		if err != nil || len(names) < 2 {
			rows.Close()
			return Authority{}, ErrUnverified
		}
		actual := map[string]string{}
		for rows.Next() {
			if len(actual) >= 256 {
				rows.Close()
				return Authority{}, ErrUnverified
			}
			values := make([]any, len(names))
			targets := make([]any, len(names))
			for i := range values {
				targets[i] = &values[i]
			}
			if rows.Scan(targets...) != nil {
				rows.Close()
				return Authority{}, ErrUnverified
			}
			actual[cellText(values[0])] = cellText(values[1])
		}
		if rows.Err() != nil || rows.Close() != nil {
			return Authority{}, ErrUnverified
		}
		for name, kind := range relation.Columns {
			if !strings.EqualFold(actual[name], kind) {
				return Authority{}, ErrUnverified
			}
		}
		// Effective SELECT under NONE, independent of disabled role grant display.
		columns := make([]string, 0, len(relation.Columns))
		for name := range relation.Columns {
			columns = append(columns, name)
		}
		sort.Strings(columns)
		for i, name := range columns {
			columns[i] = "`" + name + "`"
		}
		selected, selectErr := conn.QueryContext(ctx, "SELECT "+strings.Join(columns, ",")+" FROM "+physical+" LIMIT 0")
		if selectErr != nil {
			return Authority{}, ErrUnverified
		}
		if selected.Next() || selected.Err() != nil || selected.Close() != nil {
			return Authority{}, ErrUnverified
		}

	}
	latest, err := s.readPolicy()
	if err != nil || latest.SHA256 != a.SHA256 || ctx.Err() != nil {
		return Authority{}, ErrUnverified
	}
	return a, nil
}

func (s *Source) approveStarRocks(ctx context.Context, request Request, a Authority) (Approval, error) {
	sr := a.Policy.StarRocks
	analysis, err := starrocksguard.Compile(ctx, request.SQL, starrocksguard.Policy{Relations: sr.Relations, Functions: sr.Functions})
	if err != nil {
		return Approval{}, ErrDenied
	}
	if err := s.CheckIdentity(ctx, request.Identity); err != nil {
		return Approval{}, err
	}
	latest, err := s.readPolicy()
	if err != nil || latest.SHA256 != a.SHA256 {
		return Approval{}, ErrUnverified
	}
	expires := a.Policy.ExpiresAt
	if t := request.Identity.Snapshot().ExpiresAt; t.Before(expires) {
		expires = t
	}
	if t, ok := ctx.Deadline(); ok && t.Before(expires) {
		expires = t
	}
	if !time.Now().Before(expires) {
		return Approval{}, ErrDenied
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Approval{}, ErrUnverified
	}
	return Approval{proof: &approvalProof{issuer: s, request: request, id: "approval:" + hex.EncodeToString(random[:]), expires: expires, compiledSQL: analysis.SQL}}, nil
}

func (s *Source) executeStarRocks(ctx context.Context, p *approvalProof) (result Result, err error) {
	result = Result{Fingerprint: p.request.Fingerprint, Terminal: "failed", CleanupConfirmed: true}
	ctx, cancel := context.WithTimeout(ctx, p.request.Timeout)
	defer cancel()
	ctx, deadline := context.WithDeadline(ctx, p.expires)
	defer deadline()
	a, err := s.Authority(ctx, p.request.Identity)
	if err != nil || a.SHA256 != p.request.ScopeSHA256 {
		return result, ErrDenied
	}
	select {
	case s.slots <- struct{}{}:
	default:
		return result, ErrDenied
	}
	retain := false
	defer func() {
		if !retain {
			<-s.slots
		}
	}()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return result, ErrExecution
	}
	result.CleanupConfirmed = false
	if prepareStarRocks(ctx, conn, 30) != nil {
		result.CleanupConfirmed = conn.Close() == nil
		if !result.CleanupConfirmed {
			s.uncertain.Store(true)
			retain = true
		}
		return result, ErrUnverified
	}
	read, queryErr := sqlresult.Query(ctx, conn, p.compiledSQL, nil, sqlresult.Limits{MaxRows: p.request.MaxRows, MaxBytes: p.request.MaxBytes})
	closeErr := conn.Close() // MaxIdleConns(0) retires this exact driver connection.
	result.CleanupConfirmed = closeErr == nil && ctx.Err() == nil
	// Socket closure or cancellation is not proof of server query termination.
	// Unknown cleanup retains admission; controller must reconcile the real query.
	if !result.CleanupConfirmed {
		s.uncertain.Store(true)
		retain = true
		return result, ErrCleanup
	}
	if queryErr != nil || read.Completeness != sqlresult.Complete || read.RowsClose != sqlresult.CloseConfirmed {
		return result, ErrExecution
	}
	latest, err := s.Authority(ctx, p.request.Identity)
	if err != nil || latest.SHA256 != a.SHA256 {
		return result, ErrDenied
	}
	summary, err := resultdigest.Sum(read)
	if err != nil {
		return result, err
	}
	summary.CleanupConfirmed = true
	result.Summary = &summary
	result.Terminal = "completed"
	result.Complete = true
	// The real-business evaluation contract exports only typed counts/signatures/
	// hashes. Never return customer rows or feed execution output into the planner.
	return result, nil
}

// PlannerRelations is a metadata-only projection. It deliberately excludes
// scope filter VALUES, account identity, SQL, statistics and execution output.
type PlannerRelation struct {
	Logical       string            `json:"logical"`
	Columns       []Column          `json:"columns"`
	DateEncodings map[string]string `json:"date_encodings,omitempty"`
}

func (p Policy) PlannerRelations() []PlannerRelation {
	if p.StarRocks == nil {
		return nil
	}
	out := make([]PlannerRelation, 0, len(p.StarRocks.Relations))
	for _, r := range p.StarRocks.Relations {
		names := make([]string, 0, len(r.Columns))
		for name := range r.Columns {
			names = append(names, name)
		}
		sort.Strings(names)
		columns := make([]Column, 0, len(names))
		for _, name := range names {
			columns = append(columns, Column{Name: name, Type: r.Columns[name]})
		}
		out = append(out, PlannerRelation{Logical: r.Logical, Columns: columns, DateEncodings: r.DateEncodings})
	}
	return out
}
