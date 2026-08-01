package hostnorm_test

import (
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/http-broker/internal/hostnorm"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"already canonical", "api.github.com", "api.github.com"},
		{"uppercase folds", "API.GitHub.COM", "api.github.com"},
		{"trailing dot stripped", "api.github.com.", "api.github.com"},
		{"mixed case and trailing dot", "API.GitHub.COM.", "api.github.com"},
		{"idn to punycode", "bücher.example", "xn--bcher-kva.example"},
		{"ipv4 literal", "93.184.216.34", "93.184.216.34"},
		{"ipv4 trailing dot", "93.184.216.34.", "93.184.216.34"},
		{"ipv6 bracketed", "[::1]", "::1"},
		{"ipv6 bare", "::1", "::1"},
		{"ipv6 expanded", "0:0:0:0:0:0:0:1", "::1"},
		{"ipv6 uppercase", "[FE80::1]", "fe80::1"},
		{"ipv4-mapped ipv6 unmaps", "::ffff:127.0.0.1", "127.0.0.1"},
		{"ipv4-mapped bracketed", "[::ffff:7f00:1]", "127.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := hostnorm.Normalize(tc.in)
			if err != nil {
				t.Fatalf("Normalize(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizeCollapsesSpellings is the property the credential host-scope
// check depends on: two spellings of one address must not survive as two
// distinct strings, or a credential bound to one spelling leaks to the other.
func TestNormalizeCollapsesSpellings(t *testing.T) {
	groups := [][]string{
		{"::1", "[::1]", "0:0:0:0:0:0:0:1", "[0000:0000:0000:0000:0000:0000:0000:0001]"},
		{"127.0.0.1", "127.0.0.1.", "::ffff:127.0.0.1", "[::ffff:127.0.0.1]", "::ffff:7f00:1"},
		{"api.github.com", "API.GITHUB.COM", "api.github.com.", "Api.GitHub.Com."},
		{"fe80::1", "[FE80::1]", "[fe80:0:0:0:0:0:0:1]"},
	}
	for _, group := range groups {
		want, err := hostnorm.Normalize(group[0])
		if err != nil {
			t.Fatalf("Normalize(%q) error: %v", group[0], err)
		}
		for _, spelling := range group[1:] {
			got, err := hostnorm.Normalize(spelling)
			if err != nil {
				t.Fatalf("Normalize(%q) error: %v", spelling, err)
			}
			if got != want {
				t.Errorf("Normalize(%q) = %q, want %q (same address as %q)", spelling, got, want, group[0])
			}
		}
	}
}

func TestNormalizeIdempotent(t *testing.T) {
	inputs := []string{"API.GitHub.COM.", "bücher.example", "[::FFFF:127.0.0.1]", "[fe80::1]", "93.184.216.34."}
	for _, in := range inputs {
		once, err := hostnorm.Normalize(in)
		if err != nil {
			t.Fatalf("Normalize(%q) error: %v", in, err)
		}
		twice, err := hostnorm.Normalize(once)
		if err != nil {
			t.Fatalf("Normalize(%q) error: %v", once, err)
		}
		if once != twice {
			t.Errorf("Normalize not idempotent for %q: %q then %q", in, once, twice)
		}
	}
}

func TestNormalizeRejects(t *testing.T) {
	// A zone identifier is meaningless as a proxy target and would compare
	// unequal to the same address without a zone.
	if _, err := hostnorm.Normalize("fe80::1%eth0"); err == nil {
		t.Error("a zoned IPv6 address should not be accepted as an IP literal without error")
	}
	if _, err := hostnorm.Normalize("exa mple.com"); err == nil {
		t.Error("Normalize should reject a host containing a space")
	}
}

func TestNormalizeGlob(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"api.github.com", "api.github.com"},
		{"*.GITHUB.com", "*.github.com"},
		{"**.github.com", "**.github.com"},
		{"API-*.github.com", "api-*.github.com"},
		{"*.bücher.example", "*.xn--bcher-kva.example"},
		{"api.github.com.", "api.github.com"},
		{"[::1]", "::1"},
		{"*", "*"},
		{"**", "**"},
	}
	for _, tc := range cases {
		got, err := hostnorm.NormalizeGlob(tc.in)
		if err != nil {
			t.Fatalf("NormalizeGlob(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("NormalizeGlob(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestGlobAnchoring is the suffix-confusion case: an unanchored pattern would
// let "github.com.attacker.com" match a rule written for "github.com".
func TestGlobAnchoring(t *testing.T) {
	cases := []struct {
		pattern string
		host    string
		want    bool
	}{
		{"github.com", "github.com", true},
		{"github.com", "github.com.attacker.com", false},
		{"github.com", "attacker-github.com", false},
		{"github.com", "notgithub.com", false},
		{"github.com", "api.github.com", false},

		{"*.github.com", "api.github.com", true},
		{"*.github.com", "github.com", false},
		{"*.github.com", "a.b.github.com", false},
		{"*.github.com", "api.github.com.attacker.com", false},

		{"**.github.com", "api.github.com", true},
		{"**.github.com", "a.b.c.github.com", true},
		{"**.github.com", "github.com", true},
		{"**.github.com", "github.com.attacker.com", false},

		{"api-*.github.com", "api-v3.github.com", true},
		{"api-*.github.com", "api-v3.beta.github.com", false},

		{"*", "github", true},
		{"*", "api.github.com", false},
		{"**", "api.github.com", true},

		// Regex metacharacters in a pattern are literals, not operators.
		{"a.b.com", "axbxcom", false},
		{"127.0.0.1", "127.0.0.1", true},
		{"127.0.0.1", "127a0b0c1", false},
	}
	for _, tc := range cases {
		g, err := hostnorm.Compile(tc.pattern)
		if err != nil {
			t.Fatalf("Compile(%q) error: %v", tc.pattern, err)
		}
		if got := g.Match(tc.host); got != tc.want {
			t.Errorf("Compile(%q).Match(%q) = %v, want %v", tc.pattern, tc.host, got, tc.want)
		}
		// The one-shot helper must agree with the compiled form, or rule
		// matching and credential-scope matching could diverge (D16).
		normalized, err := hostnorm.NormalizeGlob(tc.pattern)
		if err != nil {
			t.Fatalf("NormalizeGlob(%q) error: %v", tc.pattern, err)
		}
		if got := hostnorm.MatchHostGlob(normalized, tc.host); got != tc.want {
			t.Errorf("MatchHostGlob(%q, %q) = %v, want %v", normalized, tc.host, got, tc.want)
		}
	}
}

func TestCompileRejectsBadGlob(t *testing.T) {
	if _, err := hostnorm.Compile("exa mple.com"); err == nil {
		t.Error("Compile should reject a pattern with a space in a literal segment")
	}
}

// TestNormalizeGlobRejectsUnicodeMixedSegment: a host is always compared in
// its punycode form, so a wildcard segment carrying raw Unicode could never
// match. Rejecting it at load beats loading a rule that silently never fires.
func TestNormalizeGlobRejectsUnicodeMixedSegment(t *testing.T) {
	for _, pattern := range []string{"café-*.example.com", "*-café.example.com", "bü*.example"} {
		_, err := hostnorm.NormalizeGlob(pattern)
		if err == nil {
			t.Errorf("NormalizeGlob(%q) = nil error, want it rejected", pattern)
			continue
		}
		if !strings.Contains(err.Error(), "punycode") {
			t.Errorf("NormalizeGlob(%q) error %q should tell the operator to use punycode", pattern, err)
		}
	}

	// A pure-literal Unicode segment is fine: it can be punycoded outright.
	if _, err := hostnorm.NormalizeGlob("*.bücher.example"); err != nil {
		t.Errorf("a pure-literal Unicode segment should be punycoded, not rejected: %v", err)
	}
	// An ASCII mixed segment stays supported.
	if got, err := hostnorm.NormalizeGlob("API-*.example.com"); err != nil || got != "api-*.example.com" {
		t.Errorf("NormalizeGlob(API-*.example.com) = (%q, %v), want (api-*.example.com, nil)", got, err)
	}
}
