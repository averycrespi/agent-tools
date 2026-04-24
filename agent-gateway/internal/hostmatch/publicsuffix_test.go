package hostmatch

import "testing"

func TestMatchesPublicSuffix(t *testing.T) {
	cases := []struct {
		pattern    string
		wantMatch  bool
		wantSuffix string
	}{
		// Public-suffix matches — stripped form is itself an ICANN suffix.
		{"*.com", true, "com"},
		{"*.co", true, "co"},
		{"*.io", true, "io"},
		{"**.com", true, "com"},
		{"*.co.uk", true, "co.uk"},
		{"com", true, "com"},
		// Nested / double-stripped still resolves to the suffix.
		{"*.*.com", true, "com"},

		// Not public-suffix matches — stripped form has real labels.
		{"*.example.com", false, ""},
		{"api.example.com", false, ""},
		{"*.googleapis.com", false, ""},

		// Private / non-ICANN suffixes.
		{"*.internal", false, ""},
		{"*.k8s.local", false, ""},
		{"localhost", false, ""},

		// Wildcard-only — callers reject separately, helper returns false.
		{"*", false, ""},
		{"**", false, ""},
	}
	for _, c := range cases {
		t.Run(c.pattern, func(t *testing.T) {
			gotMatch, gotSuffix := MatchesPublicSuffix(c.pattern)
			if gotMatch != c.wantMatch || gotSuffix != c.wantSuffix {
				t.Errorf("MatchesPublicSuffix(%q) = (%v, %q), want (%v, %q)",
					c.pattern, gotMatch, gotSuffix, c.wantMatch, c.wantSuffix)
			}
		})
	}
}
