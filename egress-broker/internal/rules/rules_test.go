package rules_test

import (
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/egress-broker/internal/rules"
)

func doc(rs ...rules.Rule) rules.Document {
	return rules.Document{Fallthrough: rules.FallthroughTunnel, Rules: rs}
}

// TestLoadValidation covers every AC-6 rejection case. Each subtest asserts
// both that the load fails and that the message names the offending rule, so
// an operator can find it without bisecting the file.
func TestLoadValidation(t *testing.T) {
	cases := []struct {
		name        string
		doc         rules.Document
		wantInError []string
	}{
		{
			name:        "rule missing host",
			doc:         doc(rules.Rule{Name: "no-host", Mode: rules.ModeIntercept}),
			wantInError: []string{"no-host", "host"},
		},
		{
			name:        "rule missing name",
			doc:         doc(rules.Rule{Host: "api.github.com", Mode: rules.ModeIntercept}),
			wantInError: []string{"name"},
		},
		{
			name: "tunnel rule carrying path",
			doc: doc(rules.Rule{
				Name: "tunnel-with-path", Host: "api.github.com", Path: "/x", Mode: rules.ModeTunnel,
			}),
			wantInError: []string{"tunnel-with-path", "host-only", "CONNECT"},
		},
		{
			name: "tunnel rule carrying method",
			doc: doc(rules.Rule{
				Name: "tunnel-with-method", Host: "api.github.com", Method: "GET", Mode: rules.ModeTunnel,
			}),
			wantInError: []string{"tunnel-with-method", "host-only"},
		},
		{
			name: "duplicate rule names",
			doc: doc(
				rules.Rule{Name: "dup", Host: "a.example.com", Mode: rules.ModeTunnel},
				rules.Rule{Name: "dup", Host: "b.example.com", Mode: rules.ModeTunnel},
			),
			wantInError: []string{"dup", "duplicate"},
		},
		{
			name:        "unknown mode",
			doc:         doc(rules.Rule{Name: "weird", Host: "api.github.com", Mode: rules.Mode("allow")}),
			wantInError: []string{"weird", "allow", "intercept"},
		},
		{
			name:        "missing mode",
			doc:         doc(rules.Rule{Name: "nomode", Host: "api.github.com"}),
			wantInError: []string{"nomode", "mode"},
		},
		{
			name:        "bad host glob",
			doc:         doc(rules.Rule{Name: "badglob", Host: "exa mple.com", Mode: rules.ModeTunnel}),
			wantInError: []string{"badglob", "host glob"},
		},
		{
			name:        "bad path glob",
			doc:         doc(rules.Rule{Name: "badpath", Host: "api.github.com", Path: "no-leading-slash", Mode: rules.ModeIntercept}),
			wantInError: []string{"badpath", "path glob"},
		},
		{
			name: "conflicting host-only rules",
			doc: doc(
				rules.Rule{Name: "tunnel-it", Host: "api.github.com", Mode: rules.ModeTunnel},
				rules.Rule{Name: "deny-it", Host: "api.github.com", Mode: rules.ModeDeny},
			),
			wantInError: []string{"deny-it", "tunnel-it", "unambiguous"},
		},
		{
			name:        "public suffix host",
			doc:         doc(rules.Rule{Name: "too-broad", Host: "*.com", Mode: rules.ModeTunnel}),
			wantInError: []string{"too-broad", "public suffix", "com"},
		},
		{
			name:        "public suffix host, uppercase",
			doc:         doc(rules.Rule{Name: "too-broad-upper", Host: "*.CO.UK", Mode: rules.ModeTunnel}),
			wantInError: []string{"too-broad-upper", "public suffix"},
		},
		{
			name:        "wildcard-only host",
			doc:         doc(rules.Rule{Name: "everything", Host: "**", Mode: rules.ModeTunnel}),
			wantInError: []string{"everything", "every host"},
		},
		{
			name:        "invalid method",
			doc:         doc(rules.Rule{Name: "badmethod", Host: "api.github.com", Method: "FETCH", Mode: rules.ModeIntercept}),
			wantInError: []string{"badmethod", "FETCH"},
		},
		{
			name:        "port out of range",
			doc:         doc(rules.Rule{Name: "badport", Host: "api.github.com", Ports: []int{70000}, Mode: rules.ModeTunnel}),
			wantInError: []string{"badport", "70000"},
		},
		{
			name:        "inject on a non-intercept rule",
			doc:         doc(rules.Rule{Name: "tunnel-inject", Host: "api.github.com", Mode: rules.ModeTunnel, Inject: &rules.Inject{Set: map[string]string{"A": "b"}}}),
			wantInError: []string{"tunnel-inject", "inject", "intercept"},
		},
		{
			name:        "unknown fallthrough",
			doc:         rules.Document{Fallthrough: rules.Fallthrough("allow")},
			wantInError: []string{"fallthrough", "allow"},
		},
		{
			name:        "missing fallthrough",
			doc:         rules.Document{},
			wantInError: []string{"fallthrough", "required"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rules.New(tc.doc)
			if err == nil {
				t.Fatal("New = nil error, want a validation failure")
			}
			for _, want := range tc.wantInError {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q should mention %q", err, want)
				}
			}
		})
	}
}

// TestLoadAcceptsValidDocuments guards against over-strict validation.
func TestLoadAcceptsValidDocuments(t *testing.T) {
	cases := []struct {
		name string
		doc  rules.Document
	}{
		{"empty ruleset", doc()},
		{"intercept with inject", doc(rules.Rule{
			Name: "gh", Host: "api.github.com", Path: "/repos/*/*/issues", Method: "POST",
			Mode: rules.ModeIntercept,
			Inject: &rules.Inject{
				Set:    map[string]string{"Authorization": "Bearer ${cred.gh_bot}", "X-GitHub-Api-Version": "2022-11-28"},
				Remove: []string{"X-Agent-Hint"},
			},
		})},
		{"host-only tunnel", doc(rules.Rule{Name: "pinned", Host: "*.pinned-sdk.com", Mode: rules.ModeTunnel})},
		{"host-only deny", doc(rules.Rule{Name: "ads", Host: "*.doubleclick.net", Mode: rules.ModeDeny})},
		{"path-scoped deny", doc(rules.Rule{Name: "no-admin", Host: "api.example.com", Path: "/admin/**", Mode: rules.ModeDeny})},
		{"private suffix is allowed", doc(rules.Rule{Name: "pages", Host: "*.github.io", Mode: rules.ModeTunnel})},
		{"two host-only rules of the same mode", doc(
			rules.Rule{Name: "a", Host: "api.github.com", Mode: rules.ModeTunnel},
			rules.Rule{Name: "b", Host: "api.github.com", Mode: rules.ModeTunnel},
		)},
		{"explicit ports", doc(rules.Rule{Name: "alt", Host: "api.example.com", Ports: []int{443, 8443}, Mode: rules.ModeTunnel})},
		{"deny fallthrough", rules.Document{Fallthrough: rules.FallthroughDeny}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := rules.New(tc.doc); err != nil {
				t.Fatalf("New should accept this document: %v", err)
			}
		})
	}
}

func TestRuleNames(t *testing.T) {
	e, err := rules.New(doc(
		rules.Rule{Name: "zeta", Host: "z.example.com", Mode: rules.ModeTunnel},
		rules.Rule{Name: "alpha", Host: "a.example.com", Mode: rules.ModeTunnel},
	))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := e.RuleNames()
	want := []string{"alpha", "zeta"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("RuleNames() = %v, want %v", got, want)
	}
}
