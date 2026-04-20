package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/hostnorm"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/secrets"
)

// secretRefRE matches ${secrets.<ident>} references inside inject template
// strings. Kept here (duplicated from internal/proxy) because this package
// already owns daemon-side policy warnings.
var secretRefRE = regexp.MustCompile(`\$\{secrets\.([A-Za-z_][A-Za-z0-9_]*)\}`)

// warnSecretCoverage returns soft-warning strings for rules that reference
// ${secrets.X} in their inject block where the secret's allowed_hosts may
// not cover the rule's match.host pattern. It is an approximate check —
// the authoritative scope enforcement is the runtime check in
// internal/inject — but it catches the common "bound to wrong host"
// misconfig at load time instead of first 403.
//
// Approximations:
//   - Concrete rule hosts (no wildcards) are checked via glob match
//     against each allowed_hosts pattern; if one matches, coverage is
//     confirmed. Otherwise we warn.
//   - Wildcard rule hosts ("*.github.com", "**.internal") are only
//     confirmed when the secret binds "**" (explicit all-hosts) or the
//     exact same pattern. Any other wildcard rule-host + non-identical
//     allowed_hosts combination produces a warning — rule-pattern
//     subset checking against a set of globs is non-trivial and we'd
//     rather surface a false positive than miss a real leak.
func warnSecretCoverage(ctx context.Context, engine *rules.Engine, store secrets.Store) []string {
	// Build a name → union(allowed_hosts) map once from the store.
	nameHosts, err := collectSecretHosts(ctx, store)
	if err != nil {
		return []string{fmt.Sprintf("secret coverage check: list secrets: %v", err)}
	}

	var warnings []string
	for _, r := range engine.Rules() {
		if r.Inject == nil {
			continue
		}
		refs := collectSecretRefs(r.Inject)
		if len(refs) == 0 {
			continue
		}
		normalizedRuleHost := r.Match.Host
		ruleIsConcrete := !strings.ContainsRune(normalizedRuleHost, '*')
		for _, name := range refs {
			hosts, ok := nameHosts[name]
			if !ok {
				// Missing secret is already flagged at injection time; no
				// extra warning here.
				continue
			}
			if coverageOK(normalizedRuleHost, ruleIsConcrete, hosts) {
				continue
			}
			warnings = append(warnings, fmt.Sprintf(
				"rule %q references ${secrets.%s} but that secret's allowed_hosts %v may not cover match.host %q — injection will 403 at request time if the host isn't actually covered",
				r.Name, name, hosts, normalizedRuleHost,
			))
		}
	}
	return warnings
}

// collectSecretHosts returns a map from secret name to the union of
// allowed_hosts across every scope (global and every agent:X) that carries
// a row by that name. Union is the right conservative view for a load-time
// check — at runtime only one row will be selected, but if ANY row lists an
// adequate host we prefer not to warn.
func collectSecretHosts(ctx context.Context, store secrets.Store) (map[string][]string, error) {
	metas, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string)
	seen := make(map[string]map[string]struct{})
	for _, m := range metas {
		if _, ok := seen[m.Name]; !ok {
			seen[m.Name] = make(map[string]struct{})
		}
		for _, h := range m.AllowedHosts {
			if _, dup := seen[m.Name][h]; dup {
				continue
			}
			seen[m.Name][h] = struct{}{}
			out[m.Name] = append(out[m.Name], h)
		}
	}
	return out, nil
}

// collectSecretRefs returns the unique set of secret names referenced by
// an Inject block's replace_header templates, in first-seen order.
func collectSecretRefs(inj *rules.Inject) []string {
	seen := make(map[string]struct{})
	var names []string
	for _, tmpl := range inj.ReplaceHeaders {
		for _, m := range secretRefRE.FindAllStringSubmatch(tmpl, -1) {
			name := m[1]
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	return names
}

// coverageOK reports whether a rule's host pattern is adequately covered by
// a secret's allowed_hosts. See warnSecretCoverage doc for the approximation.
func coverageOK(ruleHost string, ruleIsConcrete bool, allowedHosts []string) bool {
	for _, pat := range allowedHosts {
		if pat == "**" {
			return true
		}
		if pat == ruleHost {
			return true
		}
		if ruleIsConcrete && hostnorm.MatchHostGlob(pat, ruleHost) {
			return true
		}
	}
	return false
}
