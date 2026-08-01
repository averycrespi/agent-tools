package glob_test

import (
	"testing"

	"github.com/averycrespi/agent-tools/egress-broker/internal/glob"
)

func TestHostSeparator(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		want    bool
	}{
		{"api.github.com", "api.github.com", true},
		{"api.github.com", "api.github.com.evil.test", false},
		{"*.github.com", "api.github.com", true},
		{"*.github.com", "a.b.github.com", false},
		{"**.github.com", "a.b.github.com", true},
		{"**.github.com", "github.com", true},
	}
	for _, tc := range cases {
		g, err := glob.Compile(tc.pattern, '.')
		if err != nil {
			t.Fatalf("Compile(%q): %v", tc.pattern, err)
		}
		if got := g.Match(tc.input); got != tc.want {
			t.Errorf("Compile(%q, '.').Match(%q) = %v, want %v", tc.pattern, tc.input, got, tc.want)
		}
	}
}

func TestPathSeparator(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		want    bool
	}{
		{"/repos/*/*/issues", "/repos/octocat/hello/issues", true},
		{"/repos/*/*/issues", "/repos/octocat/hello/issues/1", false},
		{"/repos/*/*/issues", "/repos/octocat/issues", false},
		{"/admin/**", "/admin/users/42", true},
		{"/admin/**", "/admin/", true},
		{"/admin/**", "/admin", false},
		{"/admin**", "/admin", true},
		{"/admin**", "/administrate", true},
		{"/v1/users", "/v1/users", true},
		{"/v1/users", "/v2/users", false},

		// A "*" must not cross a separator, or "/admin/*" would match a
		// deeper path the operator never granted.
		{"/admin/*", "/admin/users", true},
		{"/admin/*", "/admin/users/42", false},
	}
	for _, tc := range cases {
		g, err := glob.Compile(tc.pattern, '/')
		if err != nil {
			t.Fatalf("Compile(%q): %v", tc.pattern, err)
		}
		if got := g.Match(tc.input); got != tc.want {
			t.Errorf("Compile(%q, '/').Match(%q) = %v, want %v", tc.pattern, tc.input, got, tc.want)
		}
	}
}

// TestMetacharactersAreLiterals is the injection case: a pattern is policy
// written by an operator, but the strings matched against it are attacker
// controlled. Regexp syntax in a pattern must stay inert.
func TestMetacharactersAreLiterals(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		want    bool
	}{
		{"/a+b", "/a+b", true},
		{"/a+b", "/aaab", false},
		{"/a(b)c", "/a(b)c", true},
		{"/a(b)c", "/abc", false},
		{"/a|b", "/a|b", true},
		{"/a|b", "/a", false},
		{"/a[bc]d", "/a[bc]d", true},
		{"/a[bc]d", "/abd", false},
		{"/a.b", "/a.b", true},
		{"/a.b", "/axb", false},
		{"/a$", "/a$", true},
		{"/^a", "/^a", true},
	}
	for _, tc := range cases {
		g, err := glob.Compile(tc.pattern, '/')
		if err != nil {
			t.Fatalf("Compile(%q): %v", tc.pattern, err)
		}
		if got := g.Match(tc.input); got != tc.want {
			t.Errorf("Compile(%q).Match(%q) = %v, want %v", tc.pattern, tc.input, got, tc.want)
		}
	}
}

// TestNonASCIILiterals is a regression test. Walking the pattern by byte and
// quoting each byte as its own code point turned "/café" into "^/cafÃ©$" — a
// valid regexp that never matches, so a deny rule containing any non-ASCII
// character loaded cleanly and then silently never fired.
func TestNonASCIILiterals(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		want    bool
	}{
		{"/café", "/café", true},
		{"/café", "/cafe", false},
		{"/café", "/cafÃ©", false},
		{"/naïve/**", "/naïve/deeper", true},
		{"/naïve/*", "/naïve/one", true},
		{"/naïve/*", "/naïve/one/two", false},
		{"/日本語", "/日本語", true},
		{"/日本語", "/日本", false},
		{"/*/café", "/x/café", true},
		{"/emoji/🎉", "/emoji/🎉", true},
		{"/emoji/🎉", "/emoji/x", false},
	}
	for _, tc := range cases {
		g, err := glob.Compile(tc.pattern, '/')
		if err != nil {
			t.Fatalf("Compile(%q): %v", tc.pattern, err)
		}
		if got := g.Match(tc.input); got != tc.want {
			t.Errorf("Compile(%q).Match(%q) = %v, want %v (regexp was %q)",
				tc.pattern, tc.input, got, tc.want, glob.ToRegexp(tc.pattern, '/'))
		}
	}
}

// TestNonASCIISeparatorHandling proves a multi-byte character adjacent to a
// wildcard does not disturb segment boundaries.
func TestNonASCIISeparatorHandling(t *testing.T) {
	g, err := glob.Compile("/é/*", '/')
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !g.Match("/é/x") {
		t.Error(`Match("/é/x") = false, want true`)
	}
	if g.Match("/é/x/y") {
		t.Error(`Match("/é/x/y") = true, want false: "*" must not cross a separator`)
	}
}

func TestAnchoring(t *testing.T) {
	g, err := glob.Compile("/issues", '/')
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	for _, input := range []string{"/issues/extra", "/prefix/issues", "x/issues", "/issuesx"} {
		if g.Match(input) {
			t.Errorf("Match(%q) = true, want false: patterns are anchored at both ends", input)
		}
	}
}

func TestString(t *testing.T) {
	g, err := glob.Compile("*.example.com", '.')
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if g.String() != "*.example.com" {
		t.Errorf("String() = %q, want the source pattern", g.String())
	}
}
