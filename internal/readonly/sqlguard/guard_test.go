package sqlguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// All names and grants here are synthetic parser fixtures. They do not identify
// any real business catalog, read-only account, or verified enforcement surface.
func fixturePolicy() Policy {
	return Policy{Dialect: "postgresql", Relations: map[string]Relation{
		"synthetic.visible": {Columns: []string{"id", "scope_id", "amount"}, Complete: true},
		"synthetic.subset":  {Columns: []string{"id"}, Complete: false},
	}, Functions: map[string]bool{"pg_catalog.sum": true, "pg_catalog.count": true}}
}
func TestAnalyzeSupportedAST(t *testing.T) {
	cases := map[string]string{
		"literal_semicolon":        "SELECT '; DROP TABLE synthetic.visible' AS text",
		"dollar_literal_semicolon": "SELECT $$a;b$$ AS text;",
		"comment_semicolon":        "SELECT 1 AS one /* ; */; -- ;\n",
		"explicit_columns":         "SELECT v.id, v.amount FROM synthetic.visible AS v WHERE v.scope_id = 7 ORDER BY v.id LIMIT 10",
		"wildcard_complete":        "SELECT v.* FROM synthetic.visible v",
		"subset_column":            "SELECT id FROM synthetic.subset",
		"cte":                      "WITH a AS (SELECT id, amount FROM synthetic.visible), b AS (SELECT a.id FROM a) SELECT b.id FROM b",
		"cte_shadow":               "WITH visible AS (SELECT id FROM synthetic.subset) SELECT id FROM visible",
		"join":                     "SELECT l.id FROM synthetic.visible l LEFT JOIN synthetic.subset r ON l.id = r.id",
		"derived":                  "SELECT d.id FROM (SELECT id FROM synthetic.subset) d",
		"correlated":               "SELECT v.id FROM synthetic.visible v WHERE EXISTS (SELECT s.id FROM synthetic.subset s WHERE s.id = v.id)",
		"scalar_subquery":          "SELECT (SELECT pg_catalog.count(*) AS n FROM synthetic.subset) AS n",
		"aggregate":                "SELECT pg_catalog.sum(v.amount) FILTER (WHERE v.id > 0) AS total FROM synthetic.visible v GROUP BY v.scope_id HAVING pg_catalog.count(*) > 0",
		"boolean":                  "SELECT id FROM synthetic.visible WHERE (amount IS NULL OR scope_id IS NOT NULL) AND id = 1",
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Analyze(context.Background(), sql, fixturePolicy())
			if err != nil {
				t.Fatalf("unexpected reject: %v", err)
			}
			h := sha256.Sum256([]byte(sql))
			if got.SQLSHA256 != hex.EncodeToString(h[:]) {
				t.Fatal("exact SQL digest missing")
			}
		})
	}
}
func TestAnalyzeRejectsUnsafeOrUnknownAST(t *testing.T) {
	cases := map[string]string{
		"empty": "", "parse_error": "SELECT FROM", "multi_select": "SELECT 1; SELECT 2", "multi_write": "SELECT 1; DELETE FROM synthetic.visible",
		"write": "DELETE FROM synthetic.visible", "write_cte": "WITH x AS (DELETE FROM synthetic.visible RETURNING id) SELECT id FROM x",
		"write_cte_unused": "WITH x AS (UPDATE synthetic.visible SET amount=0 RETURNING id) SELECT 1",
		"into":             "SELECT id INTO TEMP x FROM synthetic.visible", "lock": "SELECT id FROM synthetic.visible FOR UPDATE",
		"lock_share": "SELECT id FROM synthetic.visible FOR KEY SHARE", "nested_lock": "SELECT x.id FROM (SELECT id FROM synthetic.visible FOR SHARE) x",
		"cte_into":  "WITH x AS (SELECT id INTO x FROM synthetic.visible) SELECT id FROM x",
		"dangerous": "SELECT pg_catalog.pg_read_file('/etc/passwd')", "unknown_function": "SELECT pg_catalog.unknown(id) FROM synthetic.visible",
		"unknown_schema_function": "SELECT attacker.sum(amount) FROM synthetic.visible", "unqualified_function": "SELECT sum(amount) FROM synthetic.visible",
		"network": "SELECT dblink('host=example.invalid','SELECT 1')", "dynamic": "EXECUTE x", "copy": "COPY (SELECT 1) TO PROGRAM 'id'",
		"file_table": "SELECT * FROM pg_catalog.pg_ls_dir('/')", "table_function": "SELECT * FROM generate_series(1,2)",
		"relation": "SELECT id FROM synthetic.hidden", "unqualified_relation": "SELECT id FROM visible", "system_catalog": "SELECT relname FROM pg_catalog.pg_class",
		"information_schema": "SELECT table_name FROM information_schema.tables", "three_part": "SELECT id FROM db.synthetic.visible",
		"unknown_column": "SELECT secret FROM synthetic.visible", "where_column": "SELECT id FROM synthetic.visible WHERE secret=1",
		"join_column":       "SELECT l.id FROM synthetic.visible l JOIN synthetic.subset r ON r.secret=l.id",
		"join_relation":     "SELECT l.id FROM synthetic.visible l JOIN synthetic.hidden r ON r.id=l.id",
		"subquery_relation": "SELECT 1 WHERE EXISTS (SELECT id FROM synthetic.hidden)", "subquery_column": "SELECT (SELECT secret FROM synthetic.visible) AS x",
		"having_function": "SELECT id FROM synthetic.visible GROUP BY id HAVING pg_catalog.unknown(id)>0",
		"sort_function":   "SELECT id FROM synthetic.visible ORDER BY pg_catalog.pg_sleep(1)",
		"group_column":    "SELECT id FROM synthetic.visible GROUP BY secret", "limit_subquery": "SELECT id FROM synthetic.visible LIMIT (SELECT secret FROM synthetic.visible)",
		"ambiguous": "SELECT id FROM synthetic.visible a JOIN synthetic.subset b ON a.id=b.id", "duplicate_alias": "SELECT a.id FROM synthetic.visible a, synthetic.subset a",
		"unknown_alias": "SELECT z.id FROM synthetic.visible a", "hidden_table_name": "SELECT visible.id FROM synthetic.visible a",
		"wildcard_subset": "SELECT * FROM synthetic.subset", "qualified_wildcard_subset": "SELECT s.* FROM synthetic.subset s", "wildcard_mixed": "SELECT * FROM synthetic.visible v, synthetic.subset s",
		"wildcard_unknown": "SELECT z.* FROM synthetic.visible v",
		"derived_leak":     "SELECT d.secret FROM (SELECT id FROM synthetic.visible) d", "derived_unknown_inner": "SELECT d.id FROM (SELECT id, secret FROM synthetic.visible) d",
		"cte_unknown_inner": "WITH x AS (SELECT secret FROM synthetic.visible) SELECT 1", "cte_unknown_outer": "WITH x AS (SELECT id FROM synthetic.visible) SELECT secret FROM x",
		"cte_wildcard_subset": "WITH x AS (SELECT * FROM synthetic.subset) SELECT id FROM x", "cte_recursive": "WITH RECURSIVE x AS (SELECT 1 AS id) SELECT id FROM x",
		"cte_forward": "WITH a AS (SELECT id FROM b), b AS (SELECT id FROM synthetic.visible) SELECT id FROM a",
		"alias_list":  "SELECT a.secret FROM synthetic.visible a(secret)", "derived_alias_list": "SELECT a.secret FROM (SELECT id FROM synthetic.visible) a(secret)",
		"natural_join": "SELECT a.id FROM synthetic.visible a NATURAL JOIN synthetic.subset b", "using_join": "SELECT id FROM synthetic.visible JOIN synthetic.subset USING(id)",
		"lateral": "SELECT d.id FROM synthetic.visible v, LATERAL (SELECT v.id) d", "union": "SELECT id FROM synthetic.visible UNION SELECT id FROM synthetic.subset",
		"values": "VALUES (1)", "cast": "SELECT 'x'::regclass", "parameter": "SELECT $1", "window": "SELECT pg_catalog.count(*) OVER () FROM synthetic.visible",
		"operator": "SELECT id OPERATOR(attacker.=) 1 FROM synthetic.visible", "collate": "SELECT id COLLATE \"C\" FROM synthetic.visible", "tablesample": "SELECT id FROM synthetic.visible TABLESAMPLE SYSTEM(1)",
		"only": "SELECT id FROM ONLY synthetic.visible", "json": "SELECT amount->>'x' FROM synthetic.visible", "distinct": "SELECT DISTINCT id FROM synthetic.visible",
		"whole_row": "SELECT v FROM synthetic.visible v", "row_constructor": "SELECT ROW(id) FROM synthetic.visible", "select_xml": "SELECT XMLPARSE(DOCUMENT '<x/>')",
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Analyze(context.Background(), sql, fixturePolicy())
			if !errors.Is(err, ErrRejected) {
				t.Fatalf("expected rejection, got %+v, %v", got, err)
			}
			if !reflect.DeepEqual(got, Analysis{}) {
				t.Fatal("rejected query returned analysis")
			}
		})
	}
}
func TestAnalyzeProvenance(t *testing.T) {
	sql := "WITH x AS (SELECT id, amount FROM synthetic.visible) SELECT d.id, pg_catalog.sum(x.amount) AS total FROM (SELECT id FROM synthetic.subset) d JOIN x ON d.id=x.id GROUP BY d.id"
	got, err := Analyze(context.Background(), sql, fixturePolicy())
	if err != nil {
		t.Fatal(err)
	}
	want := []Column{{Name: "id", Origins: []Origin{{Relation: "synthetic.subset", Column: "id"}}}, {Name: "total", Origins: []Origin{{Relation: "synthetic.visible", Column: "amount"}}}}
	if !reflect.DeepEqual(got.Columns, want) {
		t.Fatalf("provenance %+v", got.Columns)
	}
	if !reflect.DeepEqual(got.Relations, []string{"synthetic.subset", "synthetic.visible"}) || !reflect.DeepEqual(got.Functions, []string{"pg_catalog.sum"}) {
		t.Fatalf("dependencies %+v", got)
	}
}
func TestAnalyzeScopeShadowing(t *testing.T) {
	// The inner alias exists but does not expose amount; resolving to the outer
	// alias would be both semantically incorrect and an authorization bypass.
	sql := "SELECT v.id FROM synthetic.visible v WHERE EXISTS (SELECT v.amount FROM synthetic.subset v)"
	if _, err := Analyze(context.Background(), sql, fixturePolicy()); !errors.Is(err, ErrRejected) {
		t.Fatal(err)
	}
	// An ON clause must not see a later FROM item.
	sql = "SELECT a.id FROM synthetic.visible a JOIN synthetic.subset b ON z.id=b.id, synthetic.visible z"
	if _, err := Analyze(context.Background(), sql, fixturePolicy()); !errors.Is(err, ErrRejected) {
		t.Fatal(err)
	}
}
func TestAnalyzePolicyUnverified(t *testing.T) {
	cases := map[string]Policy{"zero": {}, "dialect_unknown": fixturePolicy(), "dialect_other": fixturePolicy(), "no_catalog": fixturePolicy(), "no_functions": fixturePolicy(), "unsafe_policy_function": fixturePolicy(), "ambiguous_catalog": fixturePolicy()}
	p := cases["dialect_unknown"]
	p.Dialect = ""
	cases["dialect_unknown"] = p
	p = cases["dialect_other"]
	p.Dialect = "mysql"
	cases["dialect_other"] = p
	p = cases["no_catalog"]
	p.Relations = nil
	cases["no_catalog"] = p
	p = cases["no_functions"]
	p.Functions = nil
	cases["no_functions"] = p
	p = cases["unsafe_policy_function"]
	p.Functions["pg_catalog.pg_read_file"] = true
	cases["unsafe_policy_function"] = p
	p = cases["ambiguous_catalog"]
	p.Relations["synthetic.bad"] = Relation{Columns: []string{"id", "id"}}
	cases["ambiguous_catalog"] = p
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := Analyze(context.Background(), "SELECT 1", p); !errors.Is(err, ErrUnverified) || !reflect.DeepEqual(got, Analysis{}) {
				t.Fatalf("got %+v %v", got, err)
			}
		})
	}
}
func TestAnalyzeCancellationAndLimits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Analyze(ctx, "SELECT 1", fixturePolicy()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Analyze(context.Background(), "SELECT '"+strings.Repeat("x", 65536)+"'", fixturePolicy()); !errors.Is(err, ErrRejected) {
		t.Fatal(err)
	}
	sql := strings.Repeat("SELECT (", 70) + "SELECT 1" + strings.Repeat(")", 70)
	if _, err := Analyze(context.Background(), sql, fixturePolicy()); !errors.Is(err, ErrRejected) {
		t.Fatal(err)
	}
}
func TestAnalyzeExactSQLBinding(t *testing.T) {
	a, err := Analyze(context.Background(), "SELECT 1", fixturePolicy())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Analyze(context.Background(), "SELECT 1;", fixturePolicy())
	if err != nil {
		t.Fatal(err)
	}
	if a.SQLSHA256 == b.SQLSHA256 {
		t.Fatal("SQL byte change did not change digest")
	}
}

func TestAnalyzeIncompleteNamespaceCannotResolveUnqualified(t *testing.T) {
	cases := map[string]string{
		"outer_fallthrough":    "SELECT (SELECT amount FROM synthetic.subset s LIMIT 1) AS leaked FROM synthetic.visible v",
		"same_level_ambiguity": "SELECT amount FROM synthetic.visible v, synthetic.subset s",
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Analyze(context.Background(), sql, fixturePolicy())
			if !errors.Is(err, ErrRejected) || !reflect.DeepEqual(got, Analysis{}) {
				t.Fatalf("incomplete namespace must reject, got %+v, %v", got, err)
			}
		})
	}
}

func TestAnalyzeEmbeddedNULRejected(t *testing.T) {
	for name, sql := range map[string]string{
		"hidden_suffix": "SELECT 1\x00; DELETE FROM synthetic.visible",
		"trailing_nul":  "SELECT 1\x00",
		"literal_nul":   "SELECT 'a\x00b' AS value",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Analyze(context.Background(), sql, fixturePolicy())
			if !errors.Is(err, ErrRejected) || !reflect.DeepEqual(got, Analysis{}) {
				t.Fatalf("NUL bytes must reject, got %+v, %v", got, err)
			}
		})
	}
}

func TestAnalyzeWildcardBudget(t *testing.T) {
	t.Run("repeated_targets", func(t *testing.T) {
		sql := "SELECT " + strings.Repeat("v.*,", 4999) + "v.* FROM synthetic.visible v"
		if len(sql) >= 65536 {
			t.Fatal("fixture exceeds input bound")
		}
		got, err := Analyze(context.Background(), sql, fixturePolicy())
		if !errors.Is(err, ErrRejected) || !reflect.DeepEqual(got, Analysis{}) {
			t.Fatalf("wildcard budget must reject, got columns=%d err=%v", len(got.Columns), err)
		}
	})
	t.Run("single_wide_catalog", func(t *testing.T) {
		p := fixturePolicy()
		cols := make([]string, 4097)
		for i := range cols {
			cols[i] = fmt.Sprintf("c%d", i)
		}
		p.Relations["synthetic.wide"] = Relation{Columns: cols, Complete: true}
		got, err := Analyze(context.Background(), "SELECT w.* FROM synthetic.wide w", p)
		if !errors.Is(err, ErrRejected) || !reflect.DeepEqual(got, Analysis{}) {
			t.Fatalf("expansion budget must reject, got columns=%d err=%v", len(got.Columns), err)
		}
	})
}
