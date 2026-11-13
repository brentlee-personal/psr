package resultdigest

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"ragflow/internal/readonly/sqlresult"
	"strconv"
	"testing"
	"time"
)

func TestExistingPythonDevsetGoldenParity(t *testing.T) {
	raw, err := os.ReadFile("testdata/python_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name  string `json:"name"`
		Cells []struct {
			Kind         string `json:"kind"`
			Value        any    `json:"value"`
			DatabaseType string `json:"database_type"`
		} `json:"cells"`
		Expected string `json:"expected_set_sha256"`
	}
	if json.Unmarshal(raw, &vectors) != nil {
		t.Fatal("invalid independent golden file")
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			result := sqlresult.ReadResult{Rows: [][]any{{}}, Completeness: sqlresult.Complete, RowsClose: sqlresult.CloseConfirmed}
			for _, c := range v.Cells {
				var value any
				text, _ := c.Value.(string)
				switch c.Kind {
				case "null":
					value = nil
				case "integer":
					value, _ = strconv.ParseInt(text, 10, 64)
				case "decimal", "time":
					value = []byte(text)
				case "string":
					value = text
				case "binary":
					value, _ = hex.DecodeString(text)
				case "datetime":
					value, _ = time.Parse("2006-01-02T15:04:05.000000", text)
				case "date":
					value, _ = time.Parse("2006-01-02", text)
				default:
					t.Fatal("unknown golden type")
				}
				result.Columns = append(result.Columns, sqlresult.Column{Name: "opaque", DatabaseType: c.DatabaseType})
				result.Rows[0] = append(result.Rows[0], value)
			}
			actual, err := Sum(result)
			if err != nil {
				t.Fatal(err)
			}
			if actual.ResultSHA256 != v.Expected {
				t.Fatalf("cross-language digest mismatch: got %s expected %s", actual.ResultSHA256, v.Expected)
			}
		})
	}
}
func TestSetHashAndOrderedHashHaveExplicitDifferentSemantics(t *testing.T) {
	columns := []sqlresult.Column{{Name: "x", DatabaseType: "BIGINT"}}
	a, _ := Sum(sqlresult.ReadResult{Columns: columns, Rows: [][]any{{int64(1)}, {int64(2)}, {int64(1)}}})
	b, _ := Sum(sqlresult.ReadResult{Columns: columns, Rows: [][]any{{int64(2)}, {int64(1)}}})
	if a.ResultSHA256 != b.ResultSHA256 || a.OrderedSHA256 == b.OrderedSHA256 {
		t.Fatal("set/order distinction lost")
	}
	if a.ColumnSignatureFormat != "type-family-json-strings-v1" || a.HashFormat != "devset-ex-set-v1-decimal28" {
		t.Fatal("digest format provenance missing")
	}
}
