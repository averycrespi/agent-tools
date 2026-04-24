package proxy

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/agents"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/hostnorm"
)

// ConnectDecision is the outcome of the CONNECT-time intercept decision.
type ConnectDecision int

const (
	// DecisionTunnel means the proxy should relay traffic as a raw TCP tunnel
	// (no TLS interception).
	DecisionTunnel ConnectDecision = iota
	// DecisionMITM means the proxy should perform TLS interception.
	DecisionMITM
	// DecisionReject means the connection should be refused with 407.
	DecisionReject
)

// parseAuth extracts the bearer token from a Basic Proxy-Authorization header.
// The expected header value is: Basic base64("x:<token>") where the username
// part is ignored. Returns the token and true on success, or "", false on any
// parse error (missing, wrong scheme, bad base64, no colon separator).
func parseAuth(header string) (token string, ok bool) {
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	encoded := strings.TrimSpace(header[len(prefix):])
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", false
	}
	idx := strings.IndexByte(string(decoded), ':')
	if idx < 0 {
		return "", false
	}
	tok := string(decoded[idx+1:])
	if tok == "" {
		return "", false
	}
	return tok, true
}

// Authenticate parses the Proxy-Authorization header from hdr, extracts the
// token, and verifies it against registry. Returns the authenticated Agent on
// success, or agents.ErrInvalidToken (or another error) on failure.
func Authenticate(ctx context.Context, registry agents.Registry, hdr http.Header) (*agents.Agent, error) {
	raw := hdr.Get("Proxy-Authorization")
	tok, ok := parseAuth(raw)
	if !ok {
		return nil, agents.ErrInvalidToken
	}
	return registry.Authenticate(ctx, tok)
}

// hostGlobMatcher holds a host glob pattern and its compiled regexp.
// The semantics follow the rules package:
//   - "*"  matches any sequence within a single label (no "." crossing).
//   - "**" matches any number of labels across ".".
type hostGlobMatcher struct {
	re *regexp.Regexp
}

// compileHostGlob compiles a host glob pattern into a hostGlobMatcher.
// It reuses the same glob-to-regexp translation as the rules package.
func compileHostGlob(pattern string) hostGlobMatcher {
	re := regexp.MustCompile(hostGlobToRegexp(pattern))
	return hostGlobMatcher{re: re}
}

// hostGlobToRegexp translates a host glob pattern to an anchored regexp string.
// Separator is "." for hostname labels.
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
				// "**." matches zero or more label segments optionally followed by "."
				sb.WriteString(`(?:.*` + escapedSep + `)?`)
				i++ // consume the "."
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

// matchHostGlob reports whether host matches the given glob pattern.
func matchHostGlob(pattern, host string) bool {
	m := compileHostGlob(pattern)
	return m.re.MatchString(host)
}

// Decide returns the CONNECT-time decision for a pre-authenticated agent
// connecting to the given host. It implements the §6 decision table:
//
//	hostnorm error           → DecisionReject
//	no_intercept_hosts match → DecisionTunnel
//	no rule for agent        → DecisionTunnel
//	host is IP literal       → DecisionTunnel
//	otherwise                → DecisionMITM
//
// ag must not be nil (caller must have authenticated successfully).
// engine may be nil, in which case no rule lookup is performed (→ tunnel).
// noIntercept is the list of host-glob patterns from the config's
// no_intercept_hosts field.
func Decide(
	_ context.Context,
	host string,
	ag *agents.Agent,
	engine RulesEngine,
	noIntercept []string,
) ConnectDecision {
	// Strip port if present, then canonicalise via hostnorm. Normalization
	// is idempotent so it's safe if callers already normalised; doing it
	// here too keeps Decide self-contained.
	//
	// WHY fail closed on normalization error: a host we cannot canonicalise
	// must not be allowed to tunnel or MITM. Falling through to tunnel mode
	// with the raw host would let an IDN homograph (invalid punycode, etc.)
	// bypass every rule glob — the raw form matches nothing, so the default
	// tunnel path would pipe bytes to an attacker-controlled upstream with
	// mangled audit attribution. Reject is the only safe default.
	hostOnly := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostOnly = h
	}
	canon, err := hostnorm.Normalize(hostOnly)
	if err != nil {
		return DecisionReject
	}
	hostOnly = canon

	// no_intercept_hosts: glob-matched list → tunnel.
	for _, pat := range noIntercept {
		if matchHostGlob(pat, hostOnly) {
			return DecisionTunnel
		}
	}

	// Tunnel-on-no-rule-match invariant: if the agent is authenticated but no
	// rule for this agent targets this host, pass the bytes through as a raw
	// TCP tunnel instead of performing MITM. MITM is a consent-bearing act —
	// intercepting TLS for hosts the operator never authored a rule for would
	// silently decrypt personal/unintended traffic flowing through the proxy.
	// The safe default is: MITM only where a rule says so; tunnel everywhere
	// else.
	if engine == nil {
		return DecisionTunnel
	}
	hostsForAgent := engine.HostsForAgent(ag.Name)
	if len(hostsForAgent) == 0 {
		return DecisionTunnel
	}

	// Check whether this host matches any of the agent's rule host globs.
	matched := false
	for pat := range hostsForAgent {
		if matchHostGlob(pat, hostOnly) {
			matched = true
			break
		}
	}
	if !matched {
		return DecisionTunnel
	}

	// IP literal (v4 or v6) → tunnel. Globs are hostname-only per design.
	if net.ParseIP(hostOnly) != nil {
		return DecisionTunnel
	}

	return DecisionMITM
}
