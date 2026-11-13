// Package starrocksguard compiles a restricted MySQL SELECT against a protected
// versioned catalog. Every relation is replaced by its scoped physical source.
package starrocksguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	p "github.com/xwb1989/sqlparser"
)

var ErrRejected = errors.New("starrocks SELECT rejected")
var ident = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var integer = regexp.MustCompile(`^-?[0-9]{1,30}$`)
var isoDate = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
var compactDate = regexp.MustCompile(`^[0-9]{8}$`)

func normalizeDateValue(expr p.Expr) error {
	switch value := expr.(type) {
	case *p.SQLVal:
		if value.Type != p.StrVal && value.Type != p.IntVal {
			return ErrRejected
		}
		text := string(value.Val)
		layout := "20060102"
		if isoDate.MatchString(text) {
			layout = "2006-01-02"
		} else if !compactDate.MatchString(text) {
			return ErrRejected
		}
		parsed, err := time.Parse(layout, text)
		if err != nil {
			return ErrRejected
		}
		value.Type = p.StrVal
		value.Val = []byte(parsed.Format("20060102"))
		return nil
	case p.ValTuple:
		if len(value) == 0 {
			return ErrRejected
		}
		for _, item := range value {
			if normalizeDateValue(item) != nil {
				return ErrRejected
			}
		}
		return nil
	default:
		return ErrRejected
	}
}

type Value struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
type Relation struct {
	Logical       string             `json:"logical"`
	Physical      string             `json:"physical"`
	Columns       map[string]string  `json:"columns"`
	Filters       map[string][]Value `json:"filters"`
	DateEncodings map[string]string  `json:"date_encodings,omitempty"`
}
type Policy struct {
	Relations []Relation `json:"relations"`
	Functions []string   `json:"functions"`
}
type Analysis struct {
	SQL       string
	SQLSHA256 string
	Relations []string
}
type bound struct {
	relation Relation
	alias    string
}

func quote(s string) string { return "`" + s + "`" }
func path(s string, count int) bool {
	parts := strings.Split(s, ".")
	if len(parts) != count {
		return false
	}
	for _, x := range parts {
		if !ident.MatchString(x) {
			return false
		}
	}
	return true
}
func Validate(policy Policy) error {
	if len(policy.Relations) == 0 || len(policy.Relations) > 16 || len(policy.Functions) > 64 {
		return ErrRejected
	}
	names := map[string]bool{}
	for _, r := range policy.Relations {
		if !path(r.Logical, 2) || !path(r.Physical, 3) || names[r.Logical] || len(r.Columns) == 0 || len(r.Columns) > 256 || len(r.Filters) == 0 {
			return ErrRejected
		}
		names[r.Logical] = true
		for name, kind := range r.Columns {
			if !ident.MatchString(name) || kind == "" {
				return ErrRejected
			}
		}
		for column, values := range r.Filters {
			if r.Columns[column] == "" || len(values) == 0 || len(values) > 100 {
				return ErrRejected
			}
			for _, v := range values {
				if len(v.Value) > 192 || (v.Kind != "string" && v.Kind != "integer") || (v.Kind == "integer" && !integer.MatchString(v.Value)) {
					return ErrRejected
				}
			}
		}
		for column, encoding := range r.DateEncodings {
			if r.Columns[column] == "" || encoding != "yyyyMMdd" {
				return ErrRejected
			}
		}
	}
	for _, f := range policy.Functions {
		if !ident.MatchString(f) || dangerous(f) {
			return ErrRejected
		}
	}
	return nil
}
func dangerous(s string) bool {
	switch strings.ToLower(s) {
	case "sleep", "benchmark", "get_lock", "release_lock", "load_file", "sys_eval", "sys_exec", "last_insert_id":
		return true
	}
	return false
}

// Compile never executes SQL and never mutates the protected input policy.
// Unsupported subqueries, locks, index hints and nonallowlisted functions fail
// closed. Scope predicates are imposed on each relation before joins/aggregation.
func Compile(ctx context.Context, sql string, policy Policy) (Analysis, error) {
	if ctx == nil || ctx.Err() != nil || Validate(policy) != nil || len(sql) == 0 || len(sql) > 65536 || strings.IndexByte(sql, 0) >= 0 {
		return Analysis{}, ErrRejected
	}
	statement, err := p.Parse(sql)
	if err != nil {
		return Analysis{}, ErrRejected
	}
	root, ok := statement.(*p.Select)
	if !ok || root.Lock != "" || root.Hints != "" {
		return Analysis{}, ErrRejected
	}
	root.Comments = nil
	root.Cache = ""
	allowed := map[string]Relation{}
	functions := map[string]bool{}
	aliases := map[string]bool{}
	for _, r := range policy.Relations {
		allowed[r.Logical] = r
	}
	for _, f := range policy.Functions {
		functions[strings.ToLower(f)] = true
	}
	for _, e := range root.SelectExprs {
		if a, ok := e.(*p.AliasedExpr); ok && !a.As.IsEmpty() {
			aliases[a.As.String()] = true
		}
	}
	bindings := map[*p.AliasedTableExpr]bound{}
	byAlias := map[string]Relation{}
	used := map[string]bool{}
	err = p.Walk(func(node p.SQLNode) (bool, error) {
		if ctx.Err() != nil {
			return false, ErrRejected
		}
		switch n := node.(type) {
		case *p.Subquery:
			return false, ErrRejected
		case *p.AliasedTableExpr:
			table, ok := n.Expr.(p.TableName)
			if !ok || table.Qualifier.IsEmpty() || len(n.Partitions) > 0 || n.Hints != nil {
				return false, ErrRejected
			}
			key := table.Qualifier.String() + "." + table.Name.String()
			r, ok := allowed[key]
			if !ok {
				return false, ErrRejected
			}
			alias := n.As.String()
			if alias == "" {
				alias = table.Name.String()
			}
			if !ident.MatchString(alias) || byAlias[alias].Logical != "" {
				return false, ErrRejected
			}
			bindings[n] = bound{r, alias}
			byAlias[alias] = r
			used[key] = true
		}
		return true, nil
	}, root)
	if err != nil || len(bindings) == 0 {
		return Analysis{}, ErrRejected
	}
	resolve := func(column *p.ColName) (Relation, bool) {
		name := column.Name.String()
		if !column.Qualifier.IsEmpty() {
			q := column.Qualifier.Name.String()
			if !column.Qualifier.Qualifier.IsEmpty() {
				q = column.Qualifier.Qualifier.String() + "." + q
				for alias, r := range byAlias {
					if r.Logical == q {
						_ = alias
						return r, r.Columns[name] != ""
					}
				}
				return Relation{}, false
			}
			r, ok := byAlias[q]
			return r, ok && r.Columns[name] != ""
		}
		var found Relation
		count := 0
		for _, r := range byAlias {
			if r.Columns[name] != "" {
				found = r
				count++
			}
		}
		return found, count == 1
	}
	dateColumn := func(expr p.Expr) bool {
		column, ok := expr.(*p.ColName)
		if !ok {
			return false
		}
		relation, ok := resolve(column)
		return ok && relation.DateEncodings[column.Name.String()] == "yyyyMMdd"
	}
	err = p.Walk(func(node p.SQLNode) (bool, error) {
		if ctx.Err() != nil {
			return false, ErrRejected
		}
		switch n := node.(type) {
		case *p.ColName:
			if _, ok := resolve(n); !ok && !(n.Qualifier.IsEmpty() && aliases[n.Name.String()]) {
				return false, ErrRejected
			}
			return false, nil
		case *p.FuncExpr:
			if !n.Qualifier.IsEmpty() || !functions[strings.ToLower(n.Name.String())] || dangerous(n.Name.String()) {
				return false, ErrRejected
			}
		case *p.Nextval:
			return false, ErrRejected
		case *p.SQLVal:
			if n.Type == p.ValArg {
				return false, ErrRejected
			}
		case *p.ComparisonExpr:
			// One declared physical encoding for comparisons, IN and reversal.
			if dateColumn(n.Left) && !dateColumn(n.Right) {
				if normalizeDateValue(n.Right) != nil {
					return false, ErrRejected
				}
			}
			if dateColumn(n.Right) && !dateColumn(n.Left) {
				if normalizeDateValue(n.Left) != nil {
					return false, ErrRejected
				}
			}
		case *p.RangeCond:
			if dateColumn(n.Left) {
				if normalizeDateValue(n.From) != nil || normalizeDateValue(n.To) != nil {
					return false, ErrRejected
				}
			}
		}
		return true, nil
	}, root)
	if err != nil {
		return Analysis{}, ErrRejected
	}
	var format func(*p.TrackedBuffer, p.SQLNode)
	format = func(buf *p.TrackedBuffer, node p.SQLNode) {
		if table, ok := node.(*p.AliasedTableExpr); ok {
			b := bindings[table]
			buf.WriteString("(select ")
			selected := make([]string, 0, len(b.relation.Columns))
			for c := range b.relation.Columns {
				selected = append(selected, c)
			}
			sort.Strings(selected)
			for i, c := range selected {
				if i > 0 {
					buf.WriteString(", ")
				}
				buf.WriteString(quote(c))
			}
			buf.WriteString(" from ")
			parts := strings.Split(b.relation.Physical, ".")
			for i, x := range parts {
				if i > 0 {
					buf.WriteByte('.')
				}
				buf.WriteString(quote(x))
			}
			buf.WriteString(" where ")
			columns := make([]string, 0, len(b.relation.Filters))
			for c := range b.relation.Filters {
				columns = append(columns, c)
			}
			sort.Strings(columns)
			for i, c := range columns {
				if i > 0 {
					buf.WriteString(" and ")
				}
				buf.WriteString(quote(c) + " in (")
				for j, v := range b.relation.Filters[c] {
					if j > 0 {
						buf.WriteString(", ")
					}
					if v.Kind == "integer" {
						buf.WriteString(v.Value)
					} else {
						p.NewStrVal([]byte(v.Value)).Format(buf)
					}
				}
				buf.WriteByte(')')
			}
			buf.WriteString(") as " + quote(b.alias))
			return
		}
		if column, ok := node.(*p.ColName); ok && !column.Qualifier.Qualifier.IsEmpty() {
			logical := column.Qualifier.Qualifier.String() + "." + column.Qualifier.Name.String()
			for alias, r := range byAlias {
				if r.Logical == logical {
					buf.WriteString(quote(alias) + "." + quote(column.Name.String()))
					return
				}
			}
		}
		node.Format(buf)
	}
	buf := p.NewTrackedBuffer(format)
	buf.Myprintf("%v", root)
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.Sum256([]byte(sql))
	return Analysis{SQL: buf.String(), SQLSHA256: hex.EncodeToString(hash[:]), Relations: names}, nil
}
