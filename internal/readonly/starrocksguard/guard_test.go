package starrocksguard

import (
	"context"
	"strings"
	"testing"
)

func policy() Policy {
	return Policy{Relations: []Relation{
		{Logical: "facts.sales", Physical: "lake.facts.sales_daily", Columns: map[string]string{"group_id": "VARCHAR", "id": "BIGINT", "amount": "DECIMAL(30,10)", "dt": "VARCHAR"}, Filters: map[string][]Value{"group_id": {{Kind: "string", Value: "tenant-one"}}}, DateEncodings: map[string]string{"dt": "yyyyMMdd"}},
		{Logical: "dims.products", Physical: "lake.dims.products", Columns: map[string]string{"group_id": "BIGINT", "id": "BIGINT", "name": "VARCHAR"}, Filters: map[string][]Value{"group_id": {{Kind: "integer", Value: "17"}}}},
	}, Functions: []string{"sum", "round", "count"}}
}
func TestScopedJoinCompilesVersionedPhysicalCatalog(t *testing.T) {
	sql := "SELECT p.name, ROUND(SUM(s.amount),2) AS total FROM facts.sales s JOIN dims.products p ON s.id=p.id WHERE s.dt >= '2026-08-01' GROUP BY p.name ORDER BY total DESC LIMIT 10"
	result, err := Compile(context.Background(), sql, policy())
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"`lake`.`facts`.`sales_daily`", "`lake`.`dims`.`products`", "`group_id` in ('tenant-one')", "`group_id` in (17)", "20260801", "limit 10"} {
		if !strings.Contains(result.SQL, part) {
			t.Fatalf("missing safe compiled clause %q", part)
		}
	}
	if len(result.Relations) != 2 || len(result.SQLSHA256) != 64 {
		t.Fatal("source identity missing")
	}
}
func TestRejectsUnscopedWritesLocksSubqueriesAndUnknownCalls(t *testing.T) {
	for _, sql := range []string{"DELETE FROM facts.sales", "SELECT id FROM facts.sales; SELECT id FROM dims.products", "SELECT id FROM other.sales", "SELECT id FROM facts.sales FOR UPDATE", "SELECT sleep(2) FROM facts.sales", "SELECT id FROM facts.sales WHERE id IN (SELECT id FROM dims.products)", "SELECT private_column FROM facts.sales"} {
		if _, err := Compile(context.Background(), sql, policy()); err == nil {
			t.Fatalf("unsafe grammar admitted: %s", sql)
		}
	}
}
func TestScopeCannotBeRemovedByModelPredicate(t *testing.T) {
	result, err := Compile(context.Background(), "SELECT s.id FROM facts.sales s WHERE 1=1 OR s.group_id='other'", policy())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.SQL, "where `group_id` in ('tenant-one')) as `s`") {
		t.Fatal("scope was applied after caller predicate")
	}
}
func TestMissingOrInvalidProtectedScopeFailsClosed(t *testing.T) {
	p := policy()
	p.Relations[0].Filters = nil
	if _, err := Compile(context.Background(), "SELECT id FROM facts.sales", p); err == nil {
		t.Fatal("missing scope admitted")
	}
	p = policy()
	p.Relations[1].Filters["group_id"][0].Value = "17 OR 1=1"
	if Validate(p) == nil {
		t.Fatal("integer scope injection admitted")
	}
}

func TestDateEncodingBetweenReverseAndIn(t *testing.T) {
	for _, predicate := range []string{"s.dt BETWEEN '2026-08-10' AND '2026-08-16'", "'2026-08-10' <= s.dt AND '2026-08-16' >= s.dt", "s.dt IN ('2026-08-10','20260816')"} {
		result, err := Compile(context.Background(), "SELECT s.id FROM facts.sales s WHERE "+predicate, policy())
		if err != nil {
			t.Fatalf("supported date shape rejected: %v", err)
		}
		if strings.Contains(result.SQL, "2026-08-") || !strings.Contains(result.SQL, "20260810") || !strings.Contains(result.SQL, "20260816") {
			t.Fatal("date silently retained with incompatible physical encoding")
		}
	}
}
func TestUnsupportedOrInvalidDateDoesNotReadEmptyScopeSilently(t *testing.T) {
	for _, predicate := range []string{"dt >= '2026-02-30'", "dt >= now()", "dt BETWEEN '2026-08-10' AND 'yesterday'", "dt IN ('2026-08-10','not-a-date')"} {
		if _, err := Compile(context.Background(), "SELECT id FROM facts.sales WHERE "+predicate, policy()); err == nil {
			t.Fatal("unknown encoded date admitted")
		}
	}
}
