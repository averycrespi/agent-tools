package rules

import (
	"path"
	"slices"
	"strings"
	"sync/atomic"
)

// ConnectAction is what the proxy does with a CONNECT before any request
// exists.
type ConnectAction string

const (
	// ConnectMITM terminates TLS and evaluates each request individually.
	ConnectMITM ConnectAction = "mitm"
	// ConnectTunnel relays bytes without terminating TLS.
	ConnectTunnel ConnectAction = "tunnel"
	// ConnectDeny rejects before any upstream dial.
	ConnectDeny ConnectAction = "deny"
)

// Source values record what decided, for the audit log's source column.
//
// Attribution and action are separate axes: source says who decided, mode says
// what the proxy then did with the bytes. They were once one column, which
// meant every row no rule decided reported "fallthrough" and lost whether the
// connection was relayed untouched or terminated — the one fact a reader most
// needs.
const (
	// SourceRule marks a decision a named rule made. Rule carries which one.
	SourceRule = "rule"
	// SourceFallthrough marks a decision the unmatched-host policy made,
	// because no rule matched the host at all.
	SourceFallthrough = "fallthrough"
	// SourceImplicitAllow marks a request on a host policy knows about (some
	// path-scoped rule covers it) that matched no rule itself.
	SourceImplicitAllow = "implicit-allow"
)

// ConnectResult is the CONNECT-time decision.
type ConnectResult struct {
	Action ConnectAction
	// Rule is the name of the rule that decided this, empty unless Source is
	// SourceRule.
	Rule string
	// Source is what decided: a rule, the fallthrough policy, or an implicit
	// allow. Always set.
	Source string
	// Mode is what the proxy does with the bytes — intercept, tunnel or deny —
	// whatever decided it. Always set.
	Mode string
	// Reason is a short operator-facing explanation, used in the
	// X-Http-Broker-Reason header on a refusal.
	Reason string
	// AllowPrivate reports whether any rule matching this host and port grants
	// the dial permission to reach RFC 1918 or RFC 4193 private space.
	//
	// It is computed over every matching rule rather than taken from the rule
	// that won, for the same reason deny beats intercept: with overlapping
	// globs, reading it off the winner would let file order decide whether a
	// private dial is permitted. "Any matching rule grants it" is
	// order-independent, and it matches what is actually being granted — a
	// dial to a host and port, which is not divisible by path or method. Rules
	// carrying the flag are required to be host-only for that reason.
	AllowPrivate bool
	// MinTLSVersion is the upstream TLS floor for this connection: the lowest
	// floor any matching rule names, or DefaultMinTLSVersion when none do.
	//
	// Lowest rather than highest, so a broad rule added later cannot silently
	// raise the floor back and break a host that needs 1.2 — and, as with
	// AllowPrivate, so file order never decides.
	MinTLSVersion uint16
}

// ConnectDecision decides what to do with a CONNECT to host:port.
//
// host must already be normalised and port-stripped; rules never match a
// "host:port" string, so a rule naming a bare host matches a CONNECT on any
// port its ports list admits (AC-16).
//
// The procedure, in order:
//
//  1. Collect rules whose host glob matches and whose ports admit the port.
//  2. Any intercept match wins: the path is needed to know which rule applies,
//     and only MITM reveals it.
//  3. Otherwise a host-only tunnel or deny rule applies directly. Load-time
//     validation rejects two identically written host-only rules of differing
//     mode; differently written globs can still overlap, so deny wins here to
//     keep the outcome independent of rule order.
//  4. Otherwise only path-scoped deny rules match, so MITM and decide per
//     request. The host is already known to policy, so fallthrough does not
//     apply to it.
//  5. No rule matches the host at all: the fallthrough policy decides, itself
//     subject to the port-443 default.
func (e *Engine) ConnectDecision(host string, port int) ConnectResult {
	return e.decide(host, port, true)
}

// PlainHTTPDecision decides what to do with an absolute-form plain HTTP
// request to host:port.
//
// It is ConnectDecision with the fallthrough port limit lifted. That limit
// stops `fallthrough: "tunnel"` from turning the proxy into a general-purpose
// TCP relay (AC-16), and only a tunnel can do that: a plain HTTP request is
// parsed, evaluated and audited as one request whatever port it names. Applying
// the limit here refused every plain HTTP request to an unmatched host on the
// shipped default configuration, port 80 included.
func (e *Engine) PlainHTTPDecision(host string, port int) ConnectResult {
	return e.decide(host, port, false)
}

// decide is the shared body. tunnelPortLimit selects whether an unmatched host
// may fall through on ports other than 443.
func (e *Engine) decide(host string, port int, tunnelPortLimit bool) ConnectResult {
	var (
		intercept    *compiled
		hostOnlyDeny *compiled
		hostOnly     *compiled
		pathScoped   bool
		allowPrivate bool
		minTLS       uint16
	)

	for _, c := range e.rules {
		// A rule whose ports exclude this port simply does not apply. It is
		// not a refusal: the operator scoped it deliberately, and overriding
		// that would ignore what they wrote.
		if !c.host.Match(host) || !c.admitsPort(port) {
			continue
		}

		// Accumulated across every matching rule, before the switch picks a
		// winner: a rule that loses the action decision still grants this.
		if c.rule.AllowPrivate {
			allowPrivate = true
		}
		if v := c.rule.ResolvedMinTLSVersion(); minTLS == 0 || v < minTLS {
			minTLS = v
		}

		switch {
		case c.rule.Mode == ModeIntercept:
			if intercept == nil {
				intercept = c
			}
		case c.rule.HostOnly():
			// Two host-only rules of differing mode can still both match when
			// their globs are written differently ("api.example.com" and
			// "*.example.com"). Load-time validation only catches identical
			// patterns, so deny is preferred here rather than taking whichever
			// came first — otherwise reordering the file would silently change
			// whether traffic is tunnelled or rejected.
			if c.rule.Mode == ModeDeny {
				if hostOnlyDeny == nil {
					hostOnlyDeny = c
				}
				continue
			}
			if hostOnly == nil {
				hostOnly = c
			}
		default:
			// A path-scoped deny: coherent, because interception can precede
			// rejection. Only tunnel rules are required to be host-only.
			pathScoped = true
		}
	}

	// Step 2.
	if intercept != nil {
		return ConnectResult{
			Action: ConnectMITM, Rule: intercept.rule.Name,
			Source: SourceRule, Mode: string(ModeIntercept),
			AllowPrivate: allowPrivate, MinTLSVersion: resolveMinTLS(minTLS),
		}
	}

	// Step 3. Deny first, so the outcome does not depend on rule order.
	if hostOnlyDeny != nil {
		return ConnectResult{
			Action: ConnectDeny,
			Rule:   hostOnlyDeny.rule.Name,
			Source: SourceRule,
			Mode:   string(ModeDeny),
			Reason: "denied by rule " + hostOnlyDeny.rule.Name,
		}
	}
	if hostOnly != nil {
		return ConnectResult{
			Action: ConnectTunnel, Rule: hostOnly.rule.Name,
			Source: SourceRule, Mode: string(ModeTunnel),
			AllowPrivate: allowPrivate, MinTLSVersion: resolveMinTLS(minTLS),
		}
	}

	// Step 4.
	if pathScoped {
		return ConnectResult{
			Action: ConnectMITM, Source: SourceImplicitAllow, Mode: string(ModeIntercept),
			AllowPrivate: allowPrivate, MinTLSVersion: resolveMinTLS(minTLS),
		}
	}

	// Step 5.
	return e.fallthroughDecision(port, tunnelPortLimit)
}

// resolveMinTLS substitutes the default for "no rule named one".
func resolveMinTLS(v uint16) uint16 {
	if v == 0 {
		return DefaultMinTLSVersion
	}
	return v
}

// fallthroughDecision applies the unmatched-host policy.
//
// Tunnelling defaults to port 443 here too. Without that, "fallthrough":
// "tunnel" would make the proxy a general-purpose TCP relay for every port an
// agent cares to name, which is a much larger grant than the operator asked
// for when they chose to relay unmatched HTTPS.
func (e *Engine) fallthroughDecision(port int, tunnelPortLimit bool) ConnectResult {
	if e.fallthrough_ == FallthroughDeny {
		return ConnectResult{
			Action:        ConnectDeny,
			Source:        SourceFallthrough,
			Mode:          string(ModeDeny),
			Reason:        "no rule matches this host and fallthrough is \"deny\"",
			MinTLSVersion: DefaultMinTLSVersion,
		}
	}
	if tunnelPortLimit && port != DefaultTunnelPort {
		return ConnectResult{
			Action:        ConnectDeny,
			Source:        SourceFallthrough,
			Mode:          string(ModeDeny),
			Reason:        "fallthrough tunnelling is limited to port 443; add a rule with an explicit \"ports\" list to allow this port",
			MinTLSVersion: DefaultMinTLSVersion,
		}
	}
	return ConnectResult{
		Action: ConnectTunnel, Source: SourceFallthrough, Mode: string(ModeTunnel),
		MinTLSVersion: DefaultMinTLSVersion,
	}
}

// RequestAction is what the proxy does with an intercepted request.
type RequestAction string

const (
	// RequestForward sends the request upstream, injecting if a rule says so.
	RequestForward RequestAction = "forward"
	// RequestDeny rejects the request with 403.
	RequestDeny RequestAction = "deny"
)

// Decision is the per-request decision made after TLS termination.
type Decision struct {
	Action RequestAction
	Rule   string
	// Source and Mode are the same two axes ConnectResult carries: what
	// decided, and what the proxy does with the request.
	Source string
	Mode   string
	// Inject is the header mutation to apply, nil when there is none.
	Inject *Inject
	Reason string
}

// Evaluate decides what to do with an intercepted request.
//
// Deny beats intercept regardless of rule order (D18). Were it first-match,
// rule ordering would silently decide whether a credential reaches a path the
// operator believed was denied — and reordering a file for readability would
// change enforcement.
//
// A request matching no rule on a host policy already knows about is forwarded
// without injection, recorded as implicit-allow.
func (e *Engine) Evaluate(host string, port int, method, requestPath string) Decision {
	// Normalise once, before any rule sees it.
	requestPath = NormalizePath(requestPath)

	var intercept *compiled

	for _, c := range e.rules {
		if !c.matchesRequest(host, port, method, requestPath) {
			continue
		}
		if c.rule.Mode == ModeDeny {
			return Decision{
				Action: RequestDeny,
				Rule:   c.rule.Name,
				Source: SourceRule,
				Mode:   string(ModeDeny),
				Reason: "denied by rule " + c.rule.Name,
			}
		}
		if c.rule.Mode == ModeIntercept && intercept == nil {
			intercept = c
		}
	}

	if intercept != nil {
		return Decision{
			Action: RequestForward,
			Rule:   intercept.rule.Name,
			Source: SourceRule,
			Mode:   string(ModeIntercept),
			Inject: intercept.rule.Inject,
		}
	}

	// The connection was terminated to see this request, so the mode is
	// intercept even though no rule named it.
	return Decision{Action: RequestForward, Source: SourceImplicitAllow, Mode: string(ModeIntercept)}
}

// matchesRequest reports whether the rule applies to this request.
//
// The path is expected to be already normalised by the caller; Evaluate does
// that once per request rather than once per rule.
func (c *compiled) matchesRequest(host string, port int, method, path string) bool {
	if !c.host.Match(host) || !c.admitsPort(port) {
		return false
	}
	if c.rule.Method != "" && c.rule.Method != method {
		return false
	}
	if c.path != nil && !c.path.Match(path) {
		return false
	}
	return true
}

// NormalizePath collapses dot segments and duplicate separators before a path
// is matched against a rule.
//
// Go's URL parsing leaves "." and ".." in place, so without this a deny rule
// for "/admin/**" is bypassed by "/public/../admin/secret", "/./admin/secret",
// or "//admin/secret" — every one of which a typical upstream collapses back
// to "/admin/secret" before its own routing. Matching the collapsed form is
// the safe direction: it can only ever make a deny rule match more, never
// less.
//
// A trailing slash is preserved, because "/admin/**" matches "/admin/" but not
// "/admin", and path.Clean would otherwise silently change that.
func NormalizePath(p string) string {
	if p == "" {
		return "/"
	}

	cleaned := path.Clean(p)
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	if strings.HasSuffix(p, "/") && !strings.HasSuffix(cleaned, "/") {
		cleaned += "/"
	}
	return cleaned
}

// admitsPort reports whether the rule applies to the given port. An empty
// port set means any port, which is the default for intercept rules only.
func (c *compiled) admitsPort(port int) bool {
	if len(c.ports) == 0 {
		return true
	}
	return slices.Contains(c.ports, port)
}

// Store holds the active engine and swaps complete snapshots atomically.
//
// Readers on the request path never block on a reload, and a reload either
// installs a fully compiled snapshot or leaves the previous one serving.
type Store struct {
	engine atomic.Pointer[Engine]
}

// NewStore compiles doc and returns a store serving it.
func NewStore(doc Document) (*Store, error) {
	engine, err := New(doc)
	if err != nil {
		return nil, err
	}
	s := &Store{}
	s.engine.Store(engine)
	return s, nil
}

// Reload compiles doc and swaps it in.
//
// On a compile failure the previous snapshot stays live and the error is
// returned for the caller to log. A SIGHUP against a rules file with a typo
// must not take the sandbox's network down.
func (s *Store) Reload(doc Document) error {
	engine, err := New(doc)
	if err != nil {
		return err
	}
	s.engine.Store(engine)
	return nil
}

// Engine returns the active snapshot. Callers should take it once per
// connection and reuse it, so a reload mid-connection cannot make the CONNECT
// decision and the per-request decisions disagree.
func (s *Store) Engine() *Engine { return s.engine.Load() }
