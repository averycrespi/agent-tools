package config

import (
	"fmt"
	"strings"

	"golang.org/x/net/publicsuffix"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/hostnorm"
)

// validateConfig is the single entry point for config-level invariants.
// Called from both Load (post-merge) and Save (pre-write) so an invalid
// config is rejected on read AND can never be written from a CLI path.
//
// It mutates cfg in place to store canonical forms (e.g. host-normalized
// no_intercept_hosts entries) and returns any soft-warning messages the
// caller should surface. A non-nil error means the config is unusable.
//
// Add new validations here as separate helpers; keep them named after the
// field they guard so failures point at the offending HCL stanza.
func validateConfig(cfg *Config) ([]string, error) {
	var warnings []string
	w, err := validateNoInterceptHosts(cfg.ProxyBehavior.NoInterceptHosts)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, w...)
	return warnings, nil
}

// validateNoInterceptHosts rejects entries that match every (or nearly every)
// host. no_intercept_hosts disables MITM for matching connections — a wildcard
// entry silently disables every rule, every audit row, every injection. Any
// real entry has literal text in it (e.g. "api.example.com", "*.googleapis.com",
// "*.internal"); a pattern composed entirely of "*" and "." characters is
// always either a typo or an intentional global kill-switch, neither of which
// we want to accept.
//
// Valid entries are also normalized in place via hostnorm.NormalizeGlob so
// that rule-side and config-side globs use the same canonical form. A
// normalization difference produces a soft warning.
func validateNoInterceptHosts(patterns []string) ([]string, error) {
	var warnings []string
	for i, p := range patterns {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			return nil, fmt.Errorf(
				"proxy_behavior.no_intercept_hosts[%d]: pattern is empty",
				i,
			)
		}
		literalCount := 0
		for _, r := range trimmed {
			if r != '*' && r != '.' {
				literalCount++
			}
		}
		if literalCount == 0 {
			return nil, fmt.Errorf(
				"proxy_behavior.no_intercept_hosts[%d]: pattern %q matches every (or nearly every) host; refusing to disable MITM globally. List specific hosts (e.g. \"pinned.example.com\") or label-scoped wildcards (e.g. \"*.internal\")",
				i, p,
			)
		}
		normalized, err := hostnorm.NormalizeGlob(trimmed)
		if err != nil {
			return nil, fmt.Errorf(
				"proxy_behavior.no_intercept_hosts[%d]: %w",
				i, err,
			)
		}
		if normalized != p {
			warnings = append(warnings, fmt.Sprintf(
				"config: proxy_behavior.no_intercept_hosts[%d] %q normalized to %q",
				i, p, normalized,
			))
			patterns[i] = normalized
		}
		if w := warnIfPublicSuffix(i, patterns[i]); w != "" {
			warnings = append(warnings, w)
		}
	}
	return warnings, nil
}

// warnIfPublicSuffix returns a warning when a no_intercept_hosts pattern,
// after stripping its leading wildcard labels, reduces to an ICANN-managed
// public suffix. Such an entry would tunnel every host under a registry-
// controlled TLD past MITM — almost always a mistake (e.g. "*.com" tunnels
// the entire internet; "*.co.uk" tunnels every UK commercial domain).
//
// Patterns whose stripped form is not a public suffix (e.g. "*.example.com"),
// or whose suffix is on the private/non-ICANN portion of the PSL (e.g.
// "*.internal", "*.k8s.local"), do not produce a warning.
func warnIfPublicSuffix(idx int, pattern string) string {
	stripped := stripLeadingWildcardLabels(pattern)
	if stripped == "" {
		return ""
	}
	suffix, icann := publicsuffix.PublicSuffix(stripped)
	if !icann || stripped != suffix {
		return ""
	}
	return fmt.Sprintf(
		"config: proxy_behavior.no_intercept_hosts[%d] %q strips to public suffix %q; tunneling would bypass MITM for every host under %q",
		idx, pattern, suffix, suffix,
	)
}

// stripLeadingWildcardLabels removes any sequence of leading "*." or "**."
// label prefixes. Returns "" when the entire pattern is wildcards (callers
// reject that case separately).
func stripLeadingWildcardLabels(p string) string {
	for {
		switch {
		case strings.HasPrefix(p, "**."):
			p = p[3:]
		case strings.HasPrefix(p, "*."):
			p = p[2:]
		case p == "*" || p == "**":
			return ""
		default:
			return p
		}
	}
}
