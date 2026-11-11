package chunker

import "testing"

// QA's actual renderer passes nil options. Short destinations must retain its
// current output while the dependency also fixes explicitly enabled Safelink.
func TestSecurityQAMarkdownDefaultRenderer(t *testing.T) {
	for _, dest := range []string{"", ".", "..", "/", "./", "../", "https://example.com", "javascript:evil"} {
		t.Run(dest, func(t *testing.T) {
			want := "<p><a href=\"" + dest + "\">x</a></p>\n"
			if got := renderMarkdown("[x](" + dest + ")"); got != want {
				t.Fatalf("renderMarkdown = %q, want %q", got, want)
			}
		})
	}
}
