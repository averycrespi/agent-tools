// Package hostmatch provides host-glob analysis helpers shared between the
// config validator (no_intercept_hosts) and the secrets store (allowed_hosts).
//
// Both surfaces guard against the same footgun — a pattern like "*.com" that
// after stripping its leading wildcard labels reduces to an ICANN-managed
// public suffix, silently matching every host on the internet under that
// registry-controlled TLD. The two call sites treat the finding differently
// (config warns, secrets rejects — see each caller's WHY-comment), but the
// detection logic is identical; hoisting it here keeps the single source of
// truth for "what is a public-suffix pattern?" in one place.
package hostmatch

import (
	"strings"

	"golang.org/x/net/publicsuffix"
)

// MatchesPublicSuffix reports whether pattern, after stripping any leading
// wildcard labels ("*." / "**."), reduces to an ICANN-managed public suffix
// per the Mozilla Public Suffix List. When it does, the returned string is
// the matched suffix (e.g. "com", "co.uk"); otherwise ("", false).
//
// Examples:
//
//	"*.com"         → (true,  "com")
//	"**.co.uk"      → (true,  "co.uk")
//	"*.example.com" → (false, "")     // stripped form "example.com" is not itself a suffix
//	"*.internal"    → (false, "")     // private / non-ICANN suffix
//	"*"             → (false, "")     // callers reject wildcard-only separately
//
// The pattern is assumed to already be glob-syntax (callers have done any
// trimming / normalization upstream).
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
// label prefixes. Returns "" when the entire pattern is wildcards (callers
// reject that case separately before consulting this package).
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
