// Package hostnorm canonicalises hostnames and host-glob patterns so that
// matching is insensitive to case, trailing dots, IDN representation, and
// IPv6 spelling.
//
// Ported from the agent-gateway branch of this repository
// (origin/agent-gateway, internal/hostnorm), with three changes:
//
//   - IP literals are canonicalised through net/netip rather than passed
//     through verbatim, so "[::1]", "::1" and "0:0:0:0:0:0:0:1" all compare
//     equal. The original preserved brackets and case, which meant the same
//     address could take two forms and slip past a credential host check.
//   - Glob patterns compile once via Compile rather than being re-compiled
//     by regexp.MustCompile on every match.
//   - The glob-to-regexp translation moved to internal/glob and is not
//     duplicated here. The original carried a comment admitting it was copied
//     into internal/rules/parse.go and internal/proxy/decide.go; rule matching
//     and credential-scope matching diverging would be a silent containment
//     inconsistency (D16).
//
// Normalize operates on a bare hostname (a CONNECT target, a Host header, a
// certificate cache key). NormalizeGlob operates on the patterns written in
// a rule's "host" field and a credential's bound host list, preserving "*"
// and "**" wildcards while normalising the literal label segments.
//
// Rules:
//   - Case-fold to lowercase via the IDNA Lookup profile.
//   - Strip a single trailing "." (FQDN form).
//   - Map Unicode labels to ASCII via punycode.
//   - Canonicalise IP literals; strip IPv6 brackets.
//
// A successful Normalize(host) guarantees the result compares byte-for-byte
// equal with any other spelling of the same host. Normalize is idempotent.
package hostnorm

import (
	"fmt"
	"net/netip"
	"strings"

	"golang.org/x/net/idna"

	"github.com/averycrespi/agent-tools/egress-broker/internal/glob"
)

// Normalize returns the canonical form of host.
//
// IP literals are returned in their canonical netip form with any surrounding
// brackets removed, so callers comparing hosts (cache keys, credential scope
// checks) never see two spellings of one address.
func Normalize(host string) (string, error) {
	if host == "" {
		return "", nil
	}
	if ip, ok := asIPLiteral(host); ok {
		return ip, nil
	}

	host = strings.TrimSuffix(host, ".")
	out, err := idna.Lookup.ToASCII(host)
	if err != nil {
		return "", fmt.Errorf("hostnorm: %q: %w", host, err)
	}
	return out, nil
}

// NormalizeGlob normalises the literal label segments of a host glob pattern.
//
// Segments that are exactly "*" or "**" are preserved verbatim. Segments that
// mix "*" with literal characters (for example "api-*") are ASCII-lowercased
// but not IDNA-normalised — mixed segments must be written in ASCII.
//
// Returns an error when a pure-literal segment fails IDNA validation.
func NormalizeGlob(pattern string) (string, error) {
	if pattern == "" {
		return "", nil
	}
	// A bracketed or bare IP literal is a valid pattern; canonicalise it the
	// same way Normalize would so that a credential bound to "[::1]" matches
	// a request that normalised to "::1".
	if ip, ok := asIPLiteral(pattern); ok {
		return ip, nil
	}

	pattern = strings.TrimSuffix(pattern, ".")
	segments := strings.Split(pattern, ".")
	for i, seg := range segments {
		switch {
		case seg == "" || seg == "*" || seg == "**":
			// Empty (leading dot) or pure wildcard — leave as-is.
		case strings.Contains(seg, "*"):
			// Mixed literal+wildcard segment. There is no way to punycode the
			// literal half without knowing where the wildcard expands, so
			// these are ASCII-only and non-ASCII is rejected rather than
			// passed through: a host is compared in its punycode form, so a
			// Unicode mixed segment could never match anything, and a rule
			// that silently never fires is worse than one that fails to load.
			if !isASCII(seg) {
				return "", fmt.Errorf("hostnorm: segment %q in %q mixes a wildcard with non-ASCII characters; write the literal part in punycode (xn--…)", seg, pattern)
			}
			segments[i] = strings.ToLower(seg)
		default:
			out, err := idna.Lookup.ToASCII(seg)
			if err != nil {
				return "", fmt.Errorf("hostnorm: segment %q in %q: %w", seg, pattern, err)
			}
			segments[i] = out
		}
	}
	return strings.Join(segments, "."), nil
}

// labelSep is the host-glob segment separator.
const labelSep = '.'

// Glob is a compiled host-glob pattern. Compiling once and matching many times
// keeps the per-request path off regexp.Compile.
type Glob struct {
	g *glob.Glob
}

// Compile normalises and compiles a host glob pattern.
func Compile(pattern string) (*Glob, error) {
	normalized, err := NormalizeGlob(pattern)
	if err != nil {
		return nil, err
	}
	g, err := glob.Compile(normalized, labelSep)
	if err != nil {
		return nil, fmt.Errorf("hostnorm: %w", err)
	}
	return &Glob{g: g}, nil
}

// Match reports whether an already-normalised host matches the pattern.
func (g *Glob) Match(host string) bool { return g.g.Match(host) }

// String returns the normalised pattern the Glob was compiled from.
func (g *Glob) String() string { return g.g.String() }

// MatchHostGlob reports whether host matches pattern using the project's
// label-aware glob semantics:
//
//   - "*"  matches any sequence within a single label (it does not cross ".").
//   - "**" matches any number of labels across ".".
//
// Both arguments should already be normalised via Normalize / NormalizeGlob.
// Prefer Compile plus Glob.Match on hot paths; this helper compiles on every
// call and exists for one-shot comparisons.
func MatchHostGlob(pattern, host string) bool {
	g, err := glob.Compile(pattern, labelSep)
	if err != nil {
		return false
	}
	return g.Match(host)
}

// isASCII reports whether s contains only single-byte characters.
func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// asIPLiteral reports whether host is an IP literal — bare v4, bare v6, or
// bracketed v6 — and returns its canonical string form. Callers must not run
// IDNA processing on the result.
//
// Canonicalisation goes through netip so that every spelling of one address
// collapses to a single form. A zone identifier ("fe80::1%eth0") is rejected:
// it is meaningless as a proxy target and would otherwise compare unequal to
// the same address without a zone.
func asIPLiteral(host string) (string, bool) {
	candidate := strings.TrimSuffix(host, ".")
	if len(candidate) >= 2 && candidate[0] == '[' && candidate[len(candidate)-1] == ']' {
		candidate = candidate[1 : len(candidate)-1]
	}

	addr, err := netip.ParseAddr(candidate)
	if err != nil {
		return "", false
	}
	if addr.Zone() != "" {
		return "", false
	}
	// Unmap so ::ffff:127.0.0.1 canonicalises to 127.0.0.1 and cannot be used
	// as a second spelling that dodges a host comparison.
	return addr.Unmap().String(), true
}
