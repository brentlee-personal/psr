package resultdigest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"ragflow/internal/readonly/sqlresult"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalid = errors.New("result digest invalid")

type Summary struct {
	RowCount              int    `json:"row_count"`
	ColumnCount           int    `json:"column_count"`
	ColumnSignature       string `json:"column_signature"`
	ColumnSignatureFormat string `json:"column_signature_format"`
	ResultSHA256          string `json:"result_sha256"`
	OrderedSHA256         string `json:"ordered_sha256"`
	HashFormat            string `json:"hash_format"`
	CleanupConfirmed      bool   `json:"cleanup_confirmed"`
}

func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func canonicalJSON(value any) (string, error) {
	var out strings.Builder
	var write func(any) error
	write = func(value any) error {
		switch v := value.(type) {
		case nil:
			out.WriteString("null")
		case string:
			if !utf8.ValidString(v) {
				return ErrInvalid
			}
			out.WriteByte('"')
			for _, r := range v {
				switch r {
				case '"', '\\':
					out.WriteByte('\\')
					out.WriteRune(r)
				case '\b':
					out.WriteString(`\b`)
				case '\f':
					out.WriteString(`\f`)
				case '\n':
					out.WriteString(`\n`)
				case '\r':
					out.WriteString(`\r`)
				case '\t':
					out.WriteString(`\t`)
				default:
					if r < 32 {
						fmt.Fprintf(&out, `\u%04x`, r)
					} else {
						out.WriteRune(r)
					}
				}
			}
			out.WriteByte('"')
		case []any:
			out.WriteByte('[')
			for i, x := range v {
				if i > 0 {
					out.WriteByte(',')
				}
				if write(x) != nil {
					return ErrInvalid
				}
			}
			out.WriteByte(']')
		case [][]any:
			out.WriteByte('[')
			for i, x := range v {
				if i > 0 {
					out.WriteByte(',')
				}
				if write(x) != nil {
					return ErrInvalid
				}
			}
			out.WriteByte(']')
		case []string:
			out.WriteByte('[')
			for i, x := range v {
				if i > 0 {
					out.WriteByte(',')
				}
				if write(x) != nil {
					return ErrInvalid
				}
			}
			out.WriteByte(']')
		default:
			return ErrInvalid
		}
		return nil
	}
	if write(value) != nil {
		return "", ErrInvalid
	}
	return out.String(), nil
}

func canonicalNumber(s string) (string, error) {
	if len(s) > 1024 {
		return "", ErrInvalid
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.Atoi(s[i+1:])
		if err != nil || e < -400 || e > 400 {
			return "", ErrInvalid
		}
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return "", ErrInvalid
	}
	d := new(big.Int).Set(r.Denom())
	two, five := big.NewInt(2), big.NewInt(5)
	a, b := 0, 0
	for new(big.Int).Mod(d, two).Sign() == 0 && a < 400 {
		d.Div(d, two)
		a++
	}
	for new(big.Int).Mod(d, five).Sign() == 0 && b < 400 {
		d.Div(d, five)
		b++
	}
	if d.Cmp(big.NewInt(1)) != 0 {
		return "", ErrInvalid
	}
	if b > a {
		a = b
	}
	out := r.FloatString(a)
	if strings.Contains(out, ".") {
		out = strings.TrimRight(strings.TrimRight(out, "0"), ".")
	}
	if out == "-0" {
		out = "0"
	}
	// Existing devset-ex Decimal.normalize uses precision28, ROUND_HALF_EVEN.
	negative := strings.HasPrefix(out, "-")
	digits := strings.TrimPrefix(out, "-")
	point := strings.IndexByte(digits, '.')
	exponent := 0
	if point >= 0 {
		exponent = -(len(digits) - point - 1)
		digits = strings.ReplaceAll(digits, ".", "")
	}
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0", nil
	}
	if len(digits) > 28 {
		discard := digits[28:]
		kept := digits[:28]
		up := discard[0] > '5' || (discard[0] == '5' && (strings.Trim(discard[1:], "0") != "" || (kept[len(kept)-1]-'0')%2 == 1))
		exponent += len(discard)
		if up {
			n, _ := new(big.Int).SetString(kept, 10)
			n.Add(n, big.NewInt(1))
			kept = n.String()
		}
		digits = kept
	}
	if exponent >= 0 {
		out = digits + strings.Repeat("0", exponent)
	} else {
		point = len(digits) + exponent
		if point > 0 {
			out = digits[:point] + "." + digits[point:]
		} else {
			out = "0." + strings.Repeat("0", -point) + digits
		}
		out = strings.TrimRight(strings.TrimRight(out, "0"), ".")
	}
	if negative && out != "0" {
		out = "-" + out
	}
	return out, nil
}
func typeFamily(kind string) string {
	kind = strings.ToUpper(kind)
	switch {
	case strings.Contains(kind, "DECIMAL") || strings.Contains(kind, "NUMERIC") || strings.Contains(kind, "INT") || kind == "FLOAT" || kind == "DOUBLE":
		return "number"
	case strings.Contains(kind, "DATE") || strings.Contains(kind, "TIME"):
		return "time"
	case strings.Contains(kind, "BINARY") || strings.Contains(kind, "BLOB"):
		return "binary"
	default:
		return "string"
	}
}
func taggedCell(value any, kind string) ([]any, error) {
	if value == nil {
		return []any{"null", nil}, nil
	}
	switch v := value.(type) {
	case bool:
		if v {
			return []any{"n", "1"}, nil
		}
		return []any{"n", "0"}, nil
	case int64:
		return []any{"n", strconv.FormatInt(v, 10)}, nil
	case int:
		return []any{"n", strconv.Itoa(v)}, nil
	case uint64:
		return []any{"n", strconv.FormatUint(v, 10)}, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, ErrInvalid
		}
		n, err := canonicalNumber(strconv.FormatFloat(v, 'g', -1, 64))
		return []any{"n", n}, err
	case time.Duration:
		seconds := v.Seconds()
		format := byte('g')
		if math.Abs(seconds) >= 1e-4 && math.Abs(seconds) < 1e16 {
			format = 'f'
		}
		text := strconv.FormatFloat(seconds, format, -1, 64)
		if !strings.ContainsAny(text, ".eE") {
			text += ".0"
		}
		return []any{"t", text}, nil
	case time.Time:
		layout := "2006-01-02T15:04:05"
		if strings.EqualFold(kind, "DATE") {
			layout = "2006-01-02"
		} else if v.Nanosecond() != 0 {
			layout = "2006-01-02T15:04:05.000000"
		}
		return []any{"t", v.Format(layout)}, nil
	case []byte:
		if strings.EqualFold(kind, "TIME") {
			match := regexp.MustCompile(`^(-?)([0-9]{1,3}):([0-9]{2}):([0-9]{2})(\.[0-9]{1,6})?$`).FindStringSubmatch(string(v))
			if match == nil {
				return nil, ErrInvalid
			}
			hours, _ := strconv.Atoi(match[2])
			minutes, _ := strconv.Atoi(match[3])
			seconds, err := strconv.ParseFloat(match[4]+match[5], 64)
			if err != nil || minutes > 59 || seconds >= 60 {
				return nil, ErrInvalid
			}
			duration := time.Duration((float64(hours*3600+minutes*60)+seconds)*1e6) * time.Microsecond
			if match[1] == "-" {
				duration = -duration
			}
			return taggedCell(duration, kind)
		}

		if typeFamily(kind) == "number" {
			n, err := canonicalNumber(string(v))
			return []any{"n", n}, err
		}
		if typeFamily(kind) == "binary" {
			const chars = "0123456789abcdef"
			out := make([]byte, len(v)*2)
			for i, b := range v {
				out[2*i] = chars[b>>4]
				out[2*i+1] = chars[b&15]
			}
			return []any{"b", string(out)}, nil
		}
		return []any{"s", string(v)}, nil
	case string:
		if typeFamily(kind) == "number" {
			n, err := canonicalNumber(v)
			return []any{"n", n}, err
		}
		return []any{"s", v}, nil
	default:
		return nil, ErrInvalid
	}
}
func Sum(read sqlresult.ReadResult) (Summary, error) {
	rows := make([]string, 0, len(read.Rows))
	unique := map[string]bool{}
	types := make([]string, len(read.Columns))
	for i, c := range read.Columns {
		types[i] = typeFamily(c.DatabaseType)
	}
	for _, row := range read.Rows {
		if len(row) != len(read.Columns) {
			return Summary{}, ErrInvalid
		}
		cells := make([][]any, len(row))
		for i, v := range row {
			cell, err := taggedCell(v, read.Columns[i].DatabaseType)
			if err != nil {
				return Summary{}, err
			}
			cells[i] = cell
		}
		line, err := canonicalJSON(cells)
		if err != nil {
			return Summary{}, err
		}
		rows = append(rows, line)
		unique[line] = true
	}
	set := make([]string, 0, len(unique))
	for line := range unique {
		set = append(set, line)
	}
	sort.Strings(set)
	signature, err := canonicalJSON(types)
	if err != nil {
		return Summary{}, err
	}
	return Summary{RowCount: len(rows), ColumnCount: len(types), ColumnSignature: hash([]byte(signature)), ColumnSignatureFormat: "type-family-json-strings-v1", HashFormat: "devset-ex-set-v1-decimal28", ResultSHA256: hash([]byte(strings.Join(set, "\n"))), OrderedSHA256: hash([]byte(strings.Join(rows, "\n")))}, nil
}
