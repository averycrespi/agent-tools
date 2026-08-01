package rules

import (
	"fmt"
	"sort"

	"github.com/averycrespi/agent-tools/egress-broker/internal/credentials"
	"github.com/averycrespi/agent-tools/egress-broker/internal/hostnorm"
)

// CredentialScope is what a credential is bound to, as far as rules loading
// needs to know. Values never enter this package.
type CredentialScope struct {
	Name  string
	Hosts []string
}

// CoverageWarning reports a rule whose host glob is not obviously covered by a
// credential it injects.
type CoverageWarning struct {
	Rule       string
	Credential string
	RuleHost   string
	CredHosts  []string
}

func (w CoverageWarning) String() string {
	return fmt.Sprintf(
		"rule %q injects credential %q, but the rule's host glob %q is not covered by the credential's bound hosts %v; requests the rule matches outside that binding will be refused at injection time",
		w.Rule, w.Credential, w.RuleHost, w.CredHosts)
}

// CheckCredentialCoverage reports rules that inject a credential whose bound
// hosts do not obviously cover the rule's own host glob.
//
// This is a warning, never a load failure. The authoritative check happens per
// request, where the concrete host is known; a glob-versus-glob comparison can
// only be approximate, so failing the load would reject workable configurations.
// The warning exists because the runtime failure mode — a rule that matches and
// then 403s at injection — is otherwise only discovered by an agent hitting it.
//
// A referenced credential that no source declares is not reported here: whether
// it exists is a runtime question (a keychain item can appear later), and
// AC-8's unresolvable-credential path covers it.
func CheckCredentialCoverage(doc Document, scopes map[string]CredentialScope) []CoverageWarning {
	var warnings []CoverageWarning

	for _, rule := range doc.Rules {
		if rule.Inject == nil {
			continue
		}

		ruleHost, err := hostnorm.NormalizeGlob(rule.Host)
		if err != nil {
			// A malformed glob is reported by validation, not here.
			continue
		}

		for _, credName := range sortedReferences(rule.Inject.Set) {
			scope, known := scopes[credName]
			if !known {
				continue
			}
			if globCoveredBy(ruleHost, scope.Hosts) {
				continue
			}
			warnings = append(warnings, CoverageWarning{
				Rule:       rule.Name,
				Credential: credName,
				RuleHost:   rule.Host,
				CredHosts:  scope.Hosts,
			})
		}
	}

	return warnings
}

// globCoveredBy reports whether every host the rule glob can match is
// certainly within one of the credential's bound globs.
//
// The test is deliberately conservative in the safe direction: it reports
// "covered" only when it can show coverage, so an unusual-but-valid pattern
// produces a spurious warning rather than silence about a real gap.
//
// Two cases are decidable without enumerating hosts:
//   - The globs are identical after normalisation.
//   - The rule glob is a literal (no wildcards) that the credential glob
//     matches.
//
// Anything else — a wildcard rule glob against a differently shaped credential
// glob — is reported, because proving containment between two wildcard
// patterns is a different problem than this warning is worth.
func globCoveredBy(ruleHost string, credHosts []string) bool {
	for _, credHost := range credHosts {
		normalized, err := hostnorm.NormalizeGlob(credHost)
		if err != nil {
			continue
		}
		if normalized == ruleHost {
			return true
		}
		if !hasWildcard(ruleHost) && hostnorm.MatchHostGlob(normalized, ruleHost) {
			return true
		}
	}
	return false
}

func hasWildcard(s string) bool {
	for i := range len(s) {
		if s[i] == '*' {
			return true
		}
	}
	return false
}

// sortedReferences returns the credential names referenced by a set of header
// templates, in a stable order so warnings are deterministic. Parsing goes
// through the credentials package so the reference syntax has one definition.
func sortedReferences(templates map[string]string) []string {
	seen := make(map[string]struct{})
	for _, tmpl := range templates {
		for _, name := range credentials.References(tmpl) {
			seen[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ReferencedCredentials returns the credential names the snapshot's rules
// reference, sorted. The dashboard uses this to list credentials by name; no
// value ever enters this package.
func (e *Engine) ReferencedCredentials() []string {
	seen := make(map[string]struct{})
	for _, c := range e.rules {
		if c.rule.Inject == nil {
			continue
		}
		for _, name := range sortedReferences(c.rule.Inject.Set) {
			seen[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
