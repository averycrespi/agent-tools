package hostmatch_test

import (
	"testing"

	"github.com/averycrespi/agent-tools/egress-broker/internal/hostmatch"
)

func TestMatchesPublicSuffix(t *testing.T) {
	cases := []struct {
		pattern    string
		wantMatch  bool
		wantSuffix string
	}{
		// Reduce to an ICANN suffix — these must be rejected by callers.
		{"*.com", true, "com"},
		{"**.com", true, "com"},
		{"*.co.uk", true, "co.uk"},
		{"**.co.uk", true, "co.uk"},
		{"com", true, "com"},
		{"co.uk", true, "co.uk"},
		{"*.*.com", true, "com"},
		{"**.**.co.uk", true, "co.uk"},

		// ICANN-managed suffixes only, which is what AC-6 specifies. A private
		// PSL entry such as "github.io" is broad but is delegated by a single
		// operator rather than a registry, so it is not rejected here.
		{"*.github.io", false, ""},
		{"*.s3.amazonaws.com", false, ""},

		// Legitimate patterns.
		{"*.example.com", false, ""},
		{"api.github.com", false, ""},
		{"**.example.co.uk", false, ""},
		{"example.com", false, ""},

		// Non-ICANN / private suffixes are not the registry footgun.
		{"*.internal", false, ""},
		{"*.local", false, ""},

		// Wildcard-only: callers reject separately.
		{"*", false, ""},
		{"**", false, ""},
		{"", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			gotMatch, gotSuffix := hostmatch.MatchesPublicSuffix(tc.pattern)
			if gotMatch != tc.wantMatch || gotSuffix != tc.wantSuffix {
				t.Fatalf("MatchesPublicSuffix(%q) = (%v, %q), want (%v, %q)",
					tc.pattern, gotMatch, gotSuffix, tc.wantMatch, tc.wantSuffix)
			}
		})
	}
}
