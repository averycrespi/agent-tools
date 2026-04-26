package rules

import (
	"encoding/json"
	"regexp"
	"testing"
)

// ---- parsePath ----------------------------------------------------------------

func TestParsePath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    []pathSegment
		wantErr bool
	}{
		{
			name:  "top-level key",
			input: "remote",
			want:  []pathSegment{{key: "remote", index: -1}},
		},
		{
			name:  "nested keys",
			input: "commit.message",
			want: []pathSegment{
				{key: "commit", index: -1},
				{key: "message", index: -1},
			},
		},
		{
			name:  "integer index",
			input: "0",
			want:  []pathSegment{{index: 0}},
		},
		{
			name:  "mixed path with array index",
			input: "commit.files.0.path",
			want: []pathSegment{
				{key: "commit", index: -1},
				{key: "files", index: -1},
				{index: 0},
				{key: "path", index: -1},
			},
		},
		{
			name:    "rejects empty path",
			input:   "",
			wantErr: true,
		},
		{
			name:    "rejects empty segment a..b",
			input:   "a..b",
			wantErr: true,
		},
		{
			name:    "rejects leading dot",
			input:   ".a",
			wantErr: true,
		},
		{
			name:    "rejects trailing dot",
			input:   "a.",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parsePath(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parsePath(%q) = nil error, want error", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePath(%q) unexpected error: %v", tc.input, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parsePath(%q): got %d segments, want %d", tc.input, len(got), len(tc.want))
			}
			for i, seg := range got {
				if seg != tc.want[i] {
					t.Errorf("parsePath(%q) seg[%d] = %+v, want %+v", tc.input, i, seg, tc.want[i])
				}
			}
		})
	}
}

// ---- resolvePath ----------------------------------------------------------------

func TestResolvePath(t *testing.T) {
	t.Parallel()

	args := map[string]any{
		"remote": "origin",
		"commit": map[string]any{
			"message": "feat: new feature",
			"files": []any{
				map[string]any{"path": "main.go"},
				map[string]any{"path": "README.md"},
			},
		},
		"count": float64(42),
		"arr":   []any{"a", "b", "c"},
	}

	tests := []struct {
		name    string
		path    string
		wantVal any
		wantOK  bool
	}{
		{
			name:    "top-level key",
			path:    "remote",
			wantVal: "origin",
			wantOK:  true,
		},
		{
			name:    "nested key",
			path:    "commit.message",
			wantVal: "feat: new feature",
			wantOK:  true,
		},
		{
			name:    "integer index into array",
			path:    "arr.1",
			wantVal: "b",
			wantOK:  true,
		},
		{
			name:   "missing key returns ok=false",
			path:   "branch",
			wantOK: false,
		},
		{
			name:   "missing nested key returns ok=false",
			path:   "commit.author",
			wantOK: false,
		},
		{
			name:   "index out of range returns ok=false",
			path:   "arr.99",
			wantOK: false,
		},
		{
			name:   "type mismatch: key on array returns ok=false",
			path:   "arr.notanindex",
			wantOK: false,
		},
		{
			name:   "type mismatch: index on scalar returns ok=false",
			path:   "remote.0",
			wantOK: false,
		},
		{
			// Resolution of a non-scalar (container) node returns ok=true. The value
			// itself is the nested map — not comparable via ==, so we only check ok.
			name:   "non-scalar leaf returns container with ok=true",
			path:   "commit",
			wantOK: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			segs, err := parsePath(tc.path)
			if err != nil {
				t.Fatalf("parsePath(%q) unexpected error: %v", tc.path, err)
			}
			got, ok := resolvePath(args, segs)
			if ok != tc.wantOK {
				t.Fatalf("resolvePath ok=%v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK && tc.wantVal != nil && got != tc.wantVal {
				t.Errorf("resolvePath = %v, want %v", got, tc.wantVal)
			}
		})
	}
}

// ---- stringifyValue ----------------------------------------------------------------

func TestStringifyValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{
			name:  "string passthrough (no surrounding quotes)",
			input: "origin",
			want:  "origin",
		},
		{
			name:  "number stringified via json.Marshal",
			input: float64(42),
			want:  "42",
		},
		{
			name:  "bool true",
			input: true,
			want:  "true",
		},
		{
			name:  "bool false",
			input: false,
			want:  "false",
		},
		{
			name:  "null",
			input: nil,
			want:  "null",
		},
		{
			name:  "nested object produces compact JSON form",
			input: map[string]any{"key": "val"},
			want:  `{"key":"val"}`,
		},
		{
			name:  "array produces compact JSON form",
			input: []any{"a", "b"},
			want:  `["a","b"]`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := stringifyValue(tc.input)
			if got != tc.want {
				t.Errorf("stringifyValue(%v) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ---- decodeMatcher ----------------------------------------------------------------

func TestDecodeMatcher(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		raw       string
		wantExact *string // non-nil → expect exactMatcher with this want value
		wantRegex *string // non-nil → expect regexMatcher with this pattern
		wantErr   bool
	}{
		{
			name:      "bare string produces exactMatcher",
			raw:       `"origin"`,
			wantExact: strPtr("origin"),
		},
		{
			name:      "empty string produces exactMatcher with empty want",
			raw:       `""`,
			wantExact: strPtr(""),
		},
		{
			name:      `{"regex":"..."} produces regexMatcher`,
			raw:       `{"regex":"^feat:"}`,
			wantRegex: strPtr("^feat:"),
		},
		{
			name:    `rejects {"regex": 1} (non-string regex value)`,
			raw:     `{"regex": 1}`,
			wantErr: true,
		},
		{
			name:    `rejects {"foo": "bar"} (wrong key)`,
			raw:     `{"foo": "bar"}`,
			wantErr: true,
		},
		{
			name:    `rejects multi-key object {"regex": "x", "extra": "y"}`,
			raw:     `{"regex": "x", "extra": "y"}`,
			wantErr: true,
		},
		{
			name:    "rejects bare number",
			raw:     `42`,
			wantErr: true,
		},
		{
			name:    "rejects bare bool",
			raw:     `true`,
			wantErr: true,
		},
		{
			name:    "rejects bare array",
			raw:     `[]`,
			wantErr: true,
		},
		{
			name:    "rejects malformed regex",
			raw:     `{"regex": "[unclosed"}`,
			wantErr: true,
		},
		{
			name:    "rejects empty raw message",
			raw:     ``,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := decodeMatcher(json.RawMessage(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("decodeMatcher(%s) = nil error, want error", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeMatcher(%s) unexpected error: %v", tc.raw, err)
			}
			switch {
			case tc.wantExact != nil:
				em, ok := m.(exactMatcher)
				if !ok {
					t.Fatalf("expected exactMatcher, got %T", m)
				}
				if em.want != *tc.wantExact {
					t.Errorf("exactMatcher.want = %q, want %q", em.want, *tc.wantExact)
				}
			case tc.wantRegex != nil:
				rm, ok := m.(regexMatcher)
				if !ok {
					t.Fatalf("expected regexMatcher, got %T", m)
				}
				if rm.re.String() != *tc.wantRegex {
					t.Errorf("regexMatcher.re = %q, want %q", rm.re.String(), *tc.wantRegex)
				}
			}
		})
	}
}

// ---- exactMatcher.match ----------------------------------------------------------------

func TestExactMatcher_Match(t *testing.T) {
	t.Parallel()
	m := exactMatcher{want: "origin"}
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "equal string matches", input: "origin", want: true},
		{name: "different string does not match", input: "production", want: false},
		{name: "empty string does not match non-empty", input: "", want: false},
		{name: "substring does not match (exact only)", input: "my-origin-fork", want: false},
		{name: "prefix does not match", input: "origin-extra", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := m.match(tc.input)
			if got != tc.want {
				t.Errorf("exactMatcher.match(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// ---- regexMatcher.match ----------------------------------------------------------------

func TestRegexMatcher_Match(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pattern string
		input   string
		want    bool
	}{
		{
			name:    "anchored pattern matches full string",
			pattern: "^feat:",
			input:   "feat: add feature",
			want:    true,
		},
		{
			name:    "anchored pattern does not match non-prefix",
			pattern: "^feat:",
			input:   "fix: something",
			want:    false,
		},
		{
			name:    "full-match anchored pattern matches exactly",
			pattern: "^origin$",
			input:   "origin",
			want:    true,
		},
		{
			name:    "full-match anchored pattern rejects substring",
			pattern: "^origin$",
			input:   "my-origin-fork",
			want:    false,
		},
		// Unanchored substring footgun: this is intentional, documented behavior per
		// design § Matchers — "author-controlled anchoring". Regexes are NOT
		// auto-anchored. Authors must use ^...$ for full-match semantics.
		{
			name:    "unanchored regex matches substring (documented footgun)",
			pattern: "origin",
			input:   "my-origin-fork",
			want:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := regexMatcher{re: regexp.MustCompile(tc.pattern)}
			got := m.match(tc.input)
			if got != tc.want {
				t.Errorf("regexMatcher{%q}.match(%q) = %v, want %v", tc.pattern, tc.input, got, tc.want)
			}
		})
	}
}

// ---- compiledPattern.matchValue ----------------------------------------------------------------

func TestCompiledPattern_MatchValue(t *testing.T) {
	t.Parallel()

	args := map[string]any{
		"remote": "origin",
		"commit": map[string]any{
			"message": "feat: new feature",
		},
	}

	tests := []struct {
		name    string
		path    string
		matcher argMatcher
		want    bool
	}{
		{
			name:    "end-to-end happy path: exact match",
			path:    "remote",
			matcher: exactMatcher{want: "origin"},
			want:    true,
		},
		{
			name:    "end-to-end happy path: regex match",
			path:    "commit.message",
			matcher: regexMatcher{re: regexp.MustCompile("^feat:")},
			want:    true,
		},
		{
			name:    "returns false on path failure (missing key)",
			path:    "branch",
			matcher: exactMatcher{want: "main"},
			want:    false,
		},
		{
			name:    "returns false on matcher failure (value present but does not match)",
			path:    "remote",
			matcher: exactMatcher{want: "production"},
			want:    false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			segs, err := parsePath(tc.path)
			if err != nil {
				t.Fatalf("parsePath(%q) unexpected error: %v", tc.path, err)
			}
			p := compiledPattern{segments: segs, matcher: tc.matcher}
			got := p.matchValue(args)
			if got != tc.want {
				t.Errorf("compiledPattern.matchValue() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---- helpers ----------------------------------------------------------------

func strPtr(s string) *string { return &s }
