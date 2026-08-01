package rules_test

import (
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/http-broker/internal/rules"
)

// TestCredentialCoverageWarning is V-23: loading a rule whose host glob is not
// covered by a referenced credential's bound list warns, naming both, and does
// not fail the load.
func TestCredentialCoverageWarning(t *testing.T) {
	doc := rules.Document{
		Fallthrough: rules.FallthroughTunnel,
		Rules: []rules.Rule{{
			Name: "stripe-with-gh-token", Host: "api.stripe.com", Mode: rules.ModeIntercept,
			Inject: &rules.Inject{Set: map[string]string{"Authorization": "Bearer ${cred.gh_bot}"}},
		}},
	}
	scopes := map[string]rules.CredentialScope{
		"gh_bot": {Name: "gh_bot", Hosts: []string{"api.github.com"}},
	}

	// The load itself must still succeed: coverage is a warning, not a failure.
	if _, err := rules.New(doc); err != nil {
		t.Fatalf("New should accept the document: %v", err)
	}

	warnings := rules.CheckCredentialCoverage(doc, scopes)
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(warnings), warnings)
	}

	msg := warnings[0].String()
	for _, want := range []string{"stripe-with-gh-token", "gh_bot", "api.stripe.com", "api.github.com"} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning %q should mention %q", msg, want)
		}
	}
}

func TestCredentialCoverageNoWarningWhenCovered(t *testing.T) {
	cases := []struct {
		name      string
		ruleHost  string
		credHosts []string
	}{
		{"identical literal", "api.github.com", []string{"api.github.com"}},
		{"identical glob", "*.github.com", []string{"*.github.com"}},
		{"literal inside a credential wildcard", "api.github.com", []string{"*.github.com"}},
		{"literal inside a double wildcard", "a.b.github.com", []string{"**.github.com"}},
		{"one of several bound hosts", "api.github.com", []string{"api.stripe.com", "api.github.com"}},
		{"case differences are normalised away", "API.GitHub.com", []string{"api.github.com"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := rules.Document{
				Fallthrough: rules.FallthroughTunnel,
				Rules: []rules.Rule{{
					Name: "r", Host: tc.ruleHost, Mode: rules.ModeIntercept,
					Inject: &rules.Inject{Set: map[string]string{"Authorization": "${cred.c}"}},
				}},
			}
			scopes := map[string]rules.CredentialScope{"c": {Name: "c", Hosts: tc.credHosts}}

			if warnings := rules.CheckCredentialCoverage(doc, scopes); len(warnings) != 0 {
				t.Errorf("got warnings %v, want none", warnings)
			}
		})
	}
}

// TestCredentialCoverageWarnsWhenRuleIsBroader is the case that matters: the
// rule can match hosts the credential is not bound to, so some requests would
// 403 at injection time.
func TestCredentialCoverageWarnsWhenRuleIsBroader(t *testing.T) {
	doc := rules.Document{
		Fallthrough: rules.FallthroughTunnel,
		Rules: []rules.Rule{{
			Name: "broad", Host: "*.github.com", Mode: rules.ModeIntercept,
			Inject: &rules.Inject{Set: map[string]string{"Authorization": "${cred.c}"}},
		}},
	}
	scopes := map[string]rules.CredentialScope{"c": {Name: "c", Hosts: []string{"api.github.com"}}}

	if warnings := rules.CheckCredentialCoverage(doc, scopes); len(warnings) != 1 {
		t.Errorf("got %d warnings, want 1: a rule broader than its credential's binding should warn", len(warnings))
	}
}

func TestCredentialCoverageIgnoresUnknownCredentials(t *testing.T) {
	doc := rules.Document{
		Fallthrough: rules.FallthroughTunnel,
		Rules: []rules.Rule{{
			Name: "r", Host: "api.github.com", Mode: rules.ModeIntercept,
			Inject: &rules.Inject{Set: map[string]string{"Authorization": "${cred.not_declared}"}},
		}},
	}

	// Whether a credential exists is a runtime question — a keychain item can
	// appear after startup — so an undeclared name is not a coverage warning.
	if warnings := rules.CheckCredentialCoverage(doc, map[string]rules.CredentialScope{}); len(warnings) != 0 {
		t.Errorf("got warnings %v, want none for an undeclared credential", warnings)
	}
}

func TestCredentialCoverageIgnoresRulesWithoutInjection(t *testing.T) {
	doc := rules.Document{
		Fallthrough: rules.FallthroughTunnel,
		Rules: []rules.Rule{
			{Name: "t", Host: "api.github.com", Mode: rules.ModeTunnel},
			{Name: "d", Host: "ads.example.com", Mode: rules.ModeDeny},
		},
	}
	scopes := map[string]rules.CredentialScope{"c": {Name: "c", Hosts: []string{"api.github.com"}}}

	if warnings := rules.CheckCredentialCoverage(doc, scopes); len(warnings) != 0 {
		t.Errorf("got warnings %v, want none", warnings)
	}
}

func TestCredentialCoverageReportsEachCredentialOnce(t *testing.T) {
	doc := rules.Document{
		Fallthrough: rules.FallthroughTunnel,
		Rules: []rules.Rule{{
			Name: "r", Host: "api.stripe.com", Mode: rules.ModeIntercept,
			Inject: &rules.Inject{Set: map[string]string{
				"Authorization": "${cred.c}",
				"X-Also":        "${cred.c}",
			}},
		}},
	}
	scopes := map[string]rules.CredentialScope{"c": {Name: "c", Hosts: []string{"api.github.com"}}}

	if warnings := rules.CheckCredentialCoverage(doc, scopes); len(warnings) != 1 {
		t.Errorf("got %d warnings, want 1: a credential referenced twice should warn once", len(warnings))
	}
}
