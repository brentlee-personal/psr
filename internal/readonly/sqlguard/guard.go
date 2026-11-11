// Package sqlguard analyzes a deliberately restricted PostgreSQL SELECT AST.
// Analysis is not an execution authorization or evidence of database isolation.
package sqlguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
)

// ErrUnverified denotes a missing or unsupported server-side policy input.
var ErrUnverified = errors.New("SQL policy unverified")

// ErrRejected denotes SQL outside the supported read-only grammar or policy.
var ErrRejected = errors.New("SQL rejected")

// Relation describes authorized columns from an authoritative catalog. Complete
// means Columns contains EVERY column exposed by this relation, not a subset.
// Only complete relations may be used with a wildcard.
type Relation struct {
	Columns  []string
	Complete bool
}

// Policy is an internal, trusted-server analysis input. Never decode it from a
// request. Dialect must come from a resolved data binding; there is no default.
// Relation keys MUST be schema-qualified. Function keys MUST be pg_catalog
// qualified and independently verified read-only for the execution environment.
// Callers must not mutate maps/slices concurrently with Analyze.
// This object does not prove a principal's scope or actual database privileges.
type Policy struct {
	Dialect   string
	Relations map[string]Relation
	Functions map[string]bool
}

type Origin struct{ Relation, Column string }
type Column struct {
	Name    string
	Origins []Origin
}

// Analysis records AST-checked dependencies; it cannot be passed to an executor
// as an ApprovedQuery. SQLSHA256 hashes the exact original bytes, not deparsed SQL.
type Analysis struct {
	SQLSHA256 string
	Relations []string
	Functions []string
	Columns   []Column
}

type binding struct {
	name     string
	columns  []Column
	complete bool
}
type scope []binding
type ctes map[string][]Column
type analyzer struct {
	ctx                  context.Context
	policy               Policy
	relations, functions map[string]bool
	nodes, depth         int
}

// Analyze never performs I/O. It rejects unsupported syntax, unknown names,
// ambiguous columns, and wildcards without complete catalog coverage. Success
// means only that this restricted AST and supplied analysis policy agree.
func Analyze(ctx context.Context, sql string, policy Policy) (Analysis, error) {
	if err := ctx.Err(); err != nil {
		return Analysis{}, err
	}
	if policy.Dialect != "postgresql" || policy.Relations == nil || policy.Functions == nil {
		return Analysis{}, ErrUnverified
	}
	if len(sql) == 0 || len(sql) > 65536 {
		return Analysis{}, ErrRejected
	}
	for key, relation := range policy.Relations {
		parts := strings.Split(key, ".")
		if len(parts) != 2 || !identifier(parts[0]) || !identifier(parts[1]) || len(relation.Columns) == 0 {
			return Analysis{}, ErrUnverified
		}
		seen := map[string]bool{}
		for _, c := range relation.Columns {
			if !identifier(c) || seen[c] {
				return Analysis{}, ErrUnverified
			}
			seen[c] = true
		}
	}
	for key, allowed := range policy.Functions {
		parts := strings.Split(key, ".")
		if !allowed || len(parts) != 2 || parts[0] != "pg_catalog" || !identifier(parts[1]) || dangerous(parts[1]) {
			return Analysis{}, ErrUnverified
		}
	}
	tree, err := pg.Parse(sql)
	if err != nil {
		return Analysis{}, ErrRejected
	}
	if err = ctx.Err(); err != nil {
		return Analysis{}, err
	}
	if len(tree.Stmts) != 1 || tree.Stmts[0].Stmt.GetSelectStmt() == nil {
		return Analysis{}, ErrRejected
	}
	a := &analyzer{ctx: ctx, policy: policy, relations: map[string]bool{}, functions: map[string]bool{}}
	columns, err := a.selectStmt(tree.Stmts[0].Stmt.GetSelectStmt(), nil, ctes{})
	if err != nil {
		return Analysis{}, err
	}
	if err = ctx.Err(); err != nil {
		return Analysis{}, err
	}
	hash := sha256.Sum256([]byte(sql))
	out := Analysis{SQLSHA256: hex.EncodeToString(hash[:]), Columns: columns}
	for name := range a.relations {
		out.Relations = append(out.Relations, name)
	}
	for name := range a.functions {
		out.Functions = append(out.Functions, name)
	}
	sort.Strings(out.Relations)
	sort.Strings(out.Functions)
	return out, nil
}

func identifier(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c != '_' && (c < 'a' || c > 'z') && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}
func dangerous(s string) bool {
	// Additional defense against a misconfigured policy. Absence from this list
	// never grants permission: every callable name still needs a trusted policy.
	return strings.HasPrefix(s, "pg_") || strings.HasPrefix(s, "lo_") || strings.HasPrefix(s, "dblink") || s == "set_config" || s == "nextval" || s == "setval" || s == "query_to_xml" || s == "table_to_xml" || s == "database_to_xml" || s == "schema_to_xml"
}
func (a *analyzer) enter() error {
	if err := a.ctx.Err(); err != nil {
		return err
	}
	a.nodes++
	a.depth++
	if a.nodes > 4096 || a.depth > 64 {
		a.depth--
		return ErrRejected
	}
	return nil
}
func names(nodes []*pg.Node) ([]string, error) {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.GetString_() == nil {
			return nil, ErrRejected
		}
		out = append(out, n.GetString_().Sval)
	}
	return out, nil
}
func cloneCTEs(in ctes) ctes {
	out := ctes{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func namedColumns(cols []Column) bool {
	seen := map[string]bool{}
	for _, c := range cols {
		if !identifier(c.Name) || seen[c.Name] {
			return false
		}
		seen[c.Name] = true
	}
	return len(cols) > 0
}
func concatOrigins(all ...[]Origin) []Origin {
	var out []Origin
	seen := map[Origin]bool{}
	for _, xs := range all {
		for _, x := range xs {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Relation == out[j].Relation {
			return out[i].Column < out[j].Column
		}
		return out[i].Relation < out[j].Relation
	})
	return out
}
func mergeScope(left, right scope) (scope, error) {
	for _, l := range left {
		for _, r := range right {
			if l.name == r.name {
				return nil, ErrRejected
			}
		}
	}
	out := append(scope{}, left...)
	return append(out, right...), nil
}

func (a *analyzer) selectStmt(s *pg.SelectStmt, outer []scope, inherited ctes) ([]Column, error) {
	if err := a.enter(); err != nil {
		return nil, err
	}
	defer func() { a.depth-- }()
	if s == nil || s.IntoClause != nil || len(s.LockingClause) > 0 || s.Op != pg.SetOperation_SETOP_NONE || s.Larg != nil || s.Rarg != nil || s.All || len(s.ValuesLists) > 0 || len(s.WindowClause) > 0 || s.GroupDistinct || len(s.DistinctClause) > 0 || s.LimitOption == pg.LimitOption_LIMIT_OPTION_WITH_TIES {
		return nil, ErrRejected
	}
	localCTEs := cloneCTEs(inherited)
	if w := s.WithClause; w != nil {
		if w.Recursive {
			return nil, ErrRejected
		}
		localNames := map[string]bool{}
		for _, n := range w.Ctes {
			c := n.GetCommonTableExpr()
			if c == nil || !identifier(c.Ctename) || localNames[c.Ctename] || len(c.Aliascolnames) > 0 || c.SearchClause != nil || c.CycleClause != nil || c.Cterecursive || c.Ctequery.GetSelectStmt() == nil {
				return nil, ErrRejected
			}
			localNames[c.Ctename] = true
			cols, err := a.selectStmt(c.Ctequery.GetSelectStmt(), nil, localCTEs)
			if err != nil {
				return nil, err
			}
			if !namedColumns(cols) {
				return nil, ErrRejected
			}
			localCTEs[c.Ctename] = cols
		}
	}
	var current scope
	for _, n := range s.FromClause {
		bs, err := a.from(n, localCTEs)
		if err != nil {
			return nil, err
		}
		current, err = mergeScope(current, bs)
		if err != nil {
			return nil, err
		}
	}
	env := append([]scope{current}, outer...)
	var output []Column
	for _, n := range s.TargetList {
		r := n.GetResTarget()
		if r == nil || len(r.Indirection) > 0 {
			return nil, ErrRejected
		}
		if ref := r.Val.GetColumnRef(); ref != nil && hasStar(ref) {
			if r.Name != "" {
				return nil, ErrRejected
			}
			cols, err := wildcard(ref, current)
			if err != nil {
				return nil, err
			}
			output = append(output, cols...)
			continue
		}
		origins, err := a.expr(r.Val, env, localCTEs)
		if err != nil {
			return nil, err
		}
		name := r.Name
		if name == "" {
			if ref := r.Val.GetColumnRef(); ref != nil {
				ss, _ := names(ref.Fields)
				name = ss[len(ss)-1]
			}
		}
		output = append(output, Column{Name: name, Origins: origins})
	}
	if len(output) == 0 {
		return nil, ErrRejected
	}
	// Sort/group output aliases are intentionally unsupported; expressions resolve
	// against input columns. The policy check is therefore conservatively stricter.
	for _, n := range append(append([]*pg.Node{s.WhereClause, s.HavingClause, s.LimitOffset, s.LimitCount}, s.GroupClause...), s.SortClause...) {
		if n == nil {
			continue
		}
		if _, err := a.expr(n, env, localCTEs); err != nil {
			return nil, err
		}
	}
	return output, nil
}

func (a *analyzer) from(n *pg.Node, cs ctes) (scope, error) {
	if err := a.enter(); err != nil {
		return nil, err
	}
	defer func() { a.depth-- }()
	switch {
	case n.GetRangeVar() != nil:
		r := n.GetRangeVar()
		if r.Catalogname != "" || !identifier(r.Relname) || !r.Inh {
			return nil, ErrRejected
		}
		var cols []Column
		complete := true
		if r.Schemaname == "" {
			var ok bool
			cols, ok = cs[r.Relname]
			if !ok {
				return nil, ErrRejected
			}
		} else {
			if !identifier(r.Schemaname) || r.Schemaname == "pg_catalog" || r.Schemaname == "information_schema" || strings.HasPrefix(r.Schemaname, "pg_") {
				return nil, ErrRejected
			}
			key := r.Schemaname + "." + r.Relname
			p, ok := a.policy.Relations[key]
			if !ok {
				return nil, ErrRejected
			}
			a.relations[key] = true
			complete = p.Complete
			for _, name := range p.Columns {
				cols = append(cols, Column{Name: name, Origins: []Origin{{Relation: key, Column: name}}})
			}
		}
		alias := r.Relname
		if r.Alias != nil {
			if len(r.Alias.Colnames) > 0 || !identifier(r.Alias.Aliasname) {
				return nil, ErrRejected
			}
			alias = r.Alias.Aliasname
		}
		return scope{{name: alias, columns: cols, complete: complete}}, nil
	case n.GetRangeSubselect() != nil:
		r := n.GetRangeSubselect()
		if r.Lateral || r.Alias == nil || !identifier(r.Alias.Aliasname) || len(r.Alias.Colnames) > 0 || r.Subquery.GetSelectStmt() == nil {
			return nil, ErrRejected
		}
		cols, err := a.selectStmt(r.Subquery.GetSelectStmt(), nil, cs)
		if err != nil {
			return nil, err
		}
		if !namedColumns(cols) {
			return nil, ErrRejected
		}
		return scope{{name: r.Alias.Aliasname, columns: cols, complete: true}}, nil
	case n.GetJoinExpr() != nil:
		j := n.GetJoinExpr()
		if j.Alias != nil || j.JoinUsingAlias != nil || j.IsNatural || len(j.UsingClause) > 0 || (j.Jointype != pg.JoinType_JOIN_INNER && j.Jointype != pg.JoinType_JOIN_LEFT && j.Jointype != pg.JoinType_JOIN_RIGHT && j.Jointype != pg.JoinType_JOIN_FULL) {
			return nil, ErrRejected
		}
		l, err := a.from(j.Larg, cs)
		if err != nil {
			return nil, err
		}
		r, err := a.from(j.Rarg, cs)
		if err != nil {
			return nil, err
		}
		both, err := mergeScope(l, r)
		if err != nil {
			return nil, err
		}
		if j.Quals != nil {
			if _, err = a.expr(j.Quals, []scope{both}, cs); err != nil {
				return nil, err
			}
		}
		return both, nil
	default:
		return nil, ErrRejected
	}
}

func hasStar(r *pg.ColumnRef) bool {
	for _, f := range r.Fields {
		if f.GetAStar() != nil {
			return true
		}
	}
	return false
}
func wildcard(r *pg.ColumnRef, current scope) ([]Column, error) {
	if len(r.Fields) < 1 || len(r.Fields) > 2 || r.Fields[len(r.Fields)-1].GetAStar() == nil {
		return nil, ErrRejected
	}
	qualifier := ""
	if len(r.Fields) == 2 {
		if r.Fields[0].GetString_() == nil {
			return nil, ErrRejected
		}
		qualifier = r.Fields[0].GetString_().Sval
	}
	var cols []Column
	for _, b := range current {
		if qualifier != "" && b.name != qualifier {
			continue
		}
		if !b.complete {
			return nil, ErrRejected
		}
		cols = append(cols, b.columns...)
	}
	if len(cols) == 0 {
		return nil, ErrRejected
	}
	return cols, nil
}
func resolve(r *pg.ColumnRef, env []scope) ([]Origin, error) {
	ns, err := names(r.Fields)
	if err != nil || len(ns) < 1 || len(ns) > 2 {
		return nil, ErrRejected
	}
	for _, level := range env {
		hits := 0
		qualifierExists := false
		var found []Origin
		for _, b := range level {
			if len(ns) == 2 {
				if b.name != ns[0] {
					continue
				}
				qualifierExists = true
			}
			for _, c := range b.columns {
				if c.Name == ns[len(ns)-1] {
					hits++
					found = c.Origins
				}
			}
		}
		if hits == 1 {
			return found, nil
		}
		if hits > 1 || qualifierExists {
			return nil, ErrRejected
		}
	}
	return nil, ErrRejected
}

func (a *analyzer) expr(n *pg.Node, env []scope, cs ctes) ([]Origin, error) {
	if err := a.enter(); err != nil {
		return nil, err
	}
	defer func() { a.depth-- }()
	if n == nil {
		return nil, ErrRejected
	}
	var children []*pg.Node
	switch {
	case n.GetColumnRef() != nil:
		return resolve(n.GetColumnRef(), env)
	case n.GetAConst() != nil:
		return nil, nil
	case n.GetAExpr() != nil:
		e := n.GetAExpr()
		ns, err := names(e.Name)
		if err != nil || len(ns) != 1 || e.Kind != pg.A_Expr_Kind_AEXPR_OP {
			return nil, ErrRejected
		}
		switch ns[0] {
		case "=", "<>", "<", ">", "<=", ">=", "+", "-", "*", "/", "%":
		default:
			return nil, ErrRejected
		}
		if e.Lexpr != nil {
			children = append(children, e.Lexpr)
		}
		children = append(children, e.Rexpr)
	case n.GetBoolExpr() != nil:
		children = n.GetBoolExpr().Args
	case n.GetNullTest() != nil:
		children = []*pg.Node{n.GetNullTest().Arg}
	case n.GetBooleanTest() != nil:
		children = []*pg.Node{n.GetBooleanTest().Arg}
	case n.GetFuncCall() != nil:
		f := n.GetFuncCall()
		ns, err := names(f.Funcname)
		if err != nil || len(ns) != 2 || ns[0] != "pg_catalog" || dangerous(ns[1]) || f.Over != nil || f.FuncVariadic || f.AggWithinGroup || len(f.AggOrder) > 0 {
			return nil, ErrRejected
		}
		key := strings.Join(ns, ".")
		if !a.policy.Functions[key] {
			return nil, ErrRejected
		}
		a.functions[key] = true
		children = append(children, f.Args...)
		if f.AggFilter != nil {
			children = append(children, f.AggFilter)
		}
	case n.GetSubLink() != nil:
		s := n.GetSubLink()
		if s.SubLinkType != pg.SubLinkType_EXISTS_SUBLINK && s.SubLinkType != pg.SubLinkType_EXPR_SUBLINK {
			return nil, ErrRejected
		}
		if s.Testexpr != nil || len(s.OperName) > 0 || s.Subselect.GetSelectStmt() == nil {
			return nil, ErrRejected
		}
		cols, err := a.selectStmt(s.Subselect.GetSelectStmt(), env, cs)
		if err != nil {
			return nil, err
		}
		if s.SubLinkType == pg.SubLinkType_EXPR_SUBLINK && len(cols) != 1 {
			return nil, ErrRejected
		}
		var origins []Origin
		for _, c := range cols {
			origins = concatOrigins(origins, c.Origins)
		}
		return origins, nil
	case n.GetSortBy() != nil:
		s := n.GetSortBy()
		if len(s.UseOp) > 0 {
			return nil, ErrRejected
		}
		children = []*pg.Node{s.Node}
	default:
		return nil, ErrRejected
	}
	var origins []Origin
	for _, child := range children {
		xs, err := a.expr(child, env, cs)
		if err != nil {
			return nil, err
		}
		origins = concatOrigins(origins, xs)
	}
	return origins, nil
}
