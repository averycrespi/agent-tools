// Package hostmatch provides host-glob analysis helpers shared between rules
// loading and credential host binding.
//
// Ported from the agent-gateway branch of this repository
// (origin/agent-gateway, internal/hostmatch), unchanged in behaviour.
//
// Both surfaces guard against the same footgun: a pattern like "*.com" that,
// after its leading wildcard labels are stripped, reduces to an ICANN-managed
// public suffix — silently matching every host on the internet under a
// registry-controlled TLD. Both call sites hard-reject the finding, but the
// detection is identical, so it lives here once.
package hostmatch

import (
	"strings"

	"golang.org/x/net/publicsuffix"
)

// MatchesPublicSuffix reports whether pattern, after stripping any leading
// wildcard labels ("*." / "**."), reduces to an ICANN-managed public suffix
// per the Mozilla Public Suffix List. When it does, the returned string is the
// matched suffix (for example "com", "co.uk"); otherwise ("", false).
//
// Examples:
//
//	"*.com"         → (true,  "com")
//	"**.co.uk"      → (true,  "co.uk")
//	"*.example.com" → (false, "")     // "example.com" is not itself a suffix
//	"*.internal"    → (false, "")     // private / non-ICANN suffix
//	"*"             → (false, "")     // callers reject wildcard-only separately
//
// The pattern is assumed to already be in glob syntax; callers normalise
// upstream.
func MatchesPublicSuffix(pattern string) (bool, string) {
	stripped := stripLeadingWildcardLabels(pattern)
	if stripped == "" {
		return false, ""
	}
	suffix, icann := publicsuffix.PublicSuffix(stripped)
	if !icann || stripped != suffix {
		return false, ""
	}
	return true, suffix
}

// stripLeadingWildcardLabels removes any sequence of leading "*." or "**."
// label prefixes. Returns "" when the entire pattern is wildcards; callers
// reject that case separately before consulting this package.
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
