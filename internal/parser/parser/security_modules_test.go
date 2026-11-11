package parser

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	markdown "github.com/gomarkdown/markdown"
	mdhtml "github.com/gomarkdown/markdown/html"
	mdparser "github.com/gomarkdown/markdown/parser"
	"golang.org/x/net/html"
)

// The upstream regression covers short URL lengths. Spare capacity matters too:
// the old implementation could inspect backing bytes beyond the URL's length.
// https://github.com/gomarkdown/markdown/commit/61af6e1e69710c5ca13cfbaaaa1bc11bc763a8a9
func TestSecurityMarkdownSafeURLBounds(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"", false}, {".", false}, {"..", false},
		{"/", true}, {"./", true}, {"../", true},
		{"/path", true}, {"./path", true}, {"../path", true},
		{"https://example.com", true}, {"http://example.com", true},
		{"mailto:user@example.com", true}, {"ftp://example.com", true},
		{"javascript:alert(1)", false},
	} {
		for _, spare := range []int{0, 8} {
			t.Run(fmt.Sprintf("%q/cap+%d", tc.url, spare), func(t *testing.T) {
				url := make([]byte, len(tc.url), len(tc.url)+spare)
				copy(url, tc.url)
				if got := mdparser.IsSafeURL(url); got != tc.want {
					t.Fatalf("IsSafeURL(len=%d cap=%d) = %v, want %v", len(url), cap(url), got, tc.want)
				}
			})
		}
	}
}

func TestSecurityMarkdownRendererFlags(t *testing.T) {
	for _, dest := range []string{"", ".", "..", "/", "./", "../", "https://example.com", "javascript:evil"} {
		for _, safe := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/safe=%v", dest, safe), func(t *testing.T) {
				flags := mdhtml.Flags(0)
				if safe {
					flags = mdhtml.Safelink
				}
				got := string(markdown.ToHTML([]byte("[x]("+dest+")"), nil, mdhtml.NewRenderer(mdhtml.RendererOptions{Flags: flags})))
				want := "<p><a href=\"" + dest + "\">x</a></p>\n"
				if safe && !mdparser.IsSafeURL([]byte(dest)) {
					want = "<p><tt>x</tt></p>\n"
				}
				if got != want {
					t.Fatalf("render = %q, want %q", got, want)
				}
			})
		}
	}
}

// Adapted to the same bounded input and allocation ceiling as the upstream
// regression; no elapsed-time threshold or unbounded hostile document.
// https://go.googlesource.com/net/+/4af4fce62e93c5621a316363718b9dc78fbb0355
func TestSecurityHTMLTextCoalescing(t *testing.T) {
	const n = 10000
	for _, prefix := range []string{"", "<table>"} {
		t.Run(fmt.Sprintf("prefix=%q", prefix), func(t *testing.T) {
			input := prefix + strings.Repeat("a</x>", n)
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			doc, err := html.Parse(strings.NewReader(input))
			runtime.ReadMemStats(&after)
			if err != nil {
				t.Fatal(err)
			}
			body := doc.FirstChild.LastChild
			text := body.FirstChild
			if text == nil || text.Type != html.TextNode || text.Data != strings.Repeat("a", n) {
				t.Fatal("coalesced text was lost or reordered")
			}
			if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 5<<20 {
				t.Fatalf("Parse allocated %d bytes, want <= %d", alloc, 5<<20)
			} else {
				t.Logf("tokens=%d allocated_bytes=%d", n, alloc)
			}
			result := NewHTMLTextParser().ParseWithResult(t.Context(), "coalescing.html", []byte(input))
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			items := ItemsToMaps(result.JSON)
			wantItems := 1
			if prefix != "" {
				wantItems = 2
			}
			if len(items) != wantItems || items[0]["doc_type_kwd"] != "text" || items[0]["text"] != strings.Repeat("a", n) {
				t.Fatalf("application text output differs: items=%d, want %d", len(items), wantItems)
			}
			if prefix != "" {
				// Fostered text precedes the preserved empty table.
				requireTableRows(t, items[1], [][]string{})
			}
		})
	}
}

func BenchmarkSecurityHTMLTextCoalescing(b *testing.B) {
	for _, prefix := range []string{"", "<table>"} {
		for _, n := range []int{1000, 10000} {
			b.Run(fmt.Sprintf("prefix=%q/tokens=%d", prefix, n), func(b *testing.B) {
				input := prefix + strings.Repeat("a</x>", n)
				b.ReportAllocs()
				b.SetBytes(int64(len(input)))
				for b.Loop() {
					if _, err := html.Parse(strings.NewReader(input)); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// Tables use the application's explicit RendererOptions{} path before HTML
// normalization; retain the short-link label through both library parsers.
func TestSecurityMarkdownTableRenderer(t *testing.T) {
	for _, dest := range []string{"", ".", "..", "/", "./", "../", "https://example.com", "javascript:evil"} {
		t.Run(dest, func(t *testing.T) {
			p, err := NewMarkdownParser(GoMarkdown)
			if err != nil {
				t.Fatal(err)
			}
			input := "| H | V |\n| --- | --- |\n| [x](" + dest + ") | value |\n"
			result := p.ParseWithResult(t.Context(), "short-links.md", []byte(input))
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			items := ItemsToMaps(result.JSON)
			if len(items) != 1 {
				t.Fatalf("table items = %d, want 1", len(items))
			}
			requireTableRows(t, items[0], [][]string{{"H", "V"}, {"x", "value"}})
		})
	}
}
