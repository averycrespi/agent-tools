// Package hostnorm canonicalises hostnames and host-glob patterns so that
// matching is not sensitive to case, trailing dots, or IDN representation.
//
// Normalize operates on a bare hostname (CONNECT target, Host header, cert
// cache key). NormalizeGlob operates on the patterns written in rule
// match.host and config no_intercept_hosts, preserving "*" and "**"
// wildcards while normalising the literal label segments.
//
// Rules:
//   - Case-fold to lowercase via the IDNA Lookup profile.
//   - Strip a single trailing "." (FQDN form).
//   - Map Unicode labels to ASCII via punycode.
//   - Pass IP literals (v4, v6, bracketed v6) through unchanged.
//
// A successful Normalize(host) guarantees the result compares byte-for-byte
// equal with any other spelling of the same host. Normalize is idempotent.
package hostnorm

import (
	"fmt"
	"net"
	"regexp"
	"strings"

	"golang.org/x/net/idna"
)

// Normalize returns the canonical form of host. IP literals are returned
// unchanged (but a trailing "." is still stripped for v4 forms).
// Returns an error when host contains disallowed code points or fails IDNA
// validation.
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
// Segments that are exactly "*" or "**" are preserved verbatim. Segments that
// mix "*" with literal characters (e.g. "api-*") are ASCII-lowercased but not
// IDNA-normalised — mixed segments must be written in ASCII.
//
// Returns an error when a pure-literal segment fails IDNA validation.
func NormalizeGlob(pattern string) (string, error) {
	if pattern == "" {
		return "", nil
	}
	pattern = strings.TrimSuffix(pattern, ".")
	segments := strings.Split(pattern, ".")
	for i, seg := range segments {
		switch {
		case seg == "" || seg == "*" || seg == "**":
			// Empty (leading dot), pure wildcards — leave as-is.
		case strings.Contains(seg, "*"):
			// Mixed literal+wildcard segment: ASCII lowercase only. Unicode in
			// mixed segments is unsupported; operators must spell the
			// literal portion in ASCII.
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

// MatchHostGlob reports whether host matches the given glob pattern using the
// project's label-aware glob semantics:
//
//   - "*"  matches any sequence within a single label (no "." crossing).
//   - "**" matches any number of labels across ".".
//
// Both arguments should already be normalised via Normalize / NormalizeGlob —
// this helper does not re-normalise so callers can pre-compile patterns at
// load time.
func MatchHostGlob(pattern, host string) bool {
	re := regexp.MustCompile(hostGlobToRegexp(pattern))
	return re.MatchString(host)
}

// hostGlobToRegexp translates a host glob pattern to an anchored regexp
// string. Separator is "." for hostname labels. Kept in sync with the
// equivalent helper in internal/rules/parse.go and internal/proxy/decide.go;
// unifying those callers into this helper is tracked as a refactor.
func hostGlobToRegexp(pattern string) string {
	const sep = "."
	const escapedSep = `\.`
	var sb strings.Builder
	sb.WriteString("^")
	i := 0
	for i < len(pattern) {
		if i+1 < len(pattern) && pattern[i] == '*' && pattern[i+1] == '*' {
			i += 2
			if i < len(pattern) && string(pattern[i]) == sep {
				sb.WriteString(`(?:.*` + escapedSep + `)?`)
				i++
			} else {
				sb.WriteString(`.*`)
			}
			continue
		}
		if pattern[i] == '*' {
			sb.WriteString(`[^` + escapedSep + `]*`)
			i++
			continue
		}
		sb.WriteString(regexp.QuoteMeta(string(pattern[i])))
		i++
	}
	sb.WriteString("$")
	return sb.String()
}

// asIPLiteral reports whether host is an IP literal (v4, v6, or bracketed v6)
// and returns the canonical string form the caller should use. For IP
// literals the caller should NOT run IDNA processing.
func asIPLiteral(host string) (string, bool) {
	// Bracketed IPv6: [::1], [FE80::1] — preserve brackets and case.
	if len(host) >= 2 && host[0] == '[' && host[len(host)-1] == ']' {
		if ip := net.ParseIP(host[1 : len(host)-1]); ip != nil {
			return host, true
		}
	}
	// Bare IPv4/IPv6 literal — accept a single trailing dot for v4.
	stripped := strings.TrimSuffix(host, ".")
	if ip := net.ParseIP(stripped); ip != nil {
		return stripped, true
	}
	return "", false
}
