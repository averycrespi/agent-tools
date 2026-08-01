package rules

import (
	"slices"
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

// Attribution values recorded in the audit log's mode column.
const (
	// ModeFallthrough marks a decision made by the fallthrough policy rather
	// than by any rule.
	ModeFallthrough = "fallthrough"
	// ModeImplicitAllow marks a request on a host that policy knows about
	// (some path-scoped rule covers it) but which matched no rule itself.
	ModeImplicitAllow = "implicit-allow"
)

// ConnectResult is the CONNECT-time decision.
type ConnectResult struct {
	Action ConnectAction
	// Rule is the name of the rule that decided this, empty when the
	// fallthrough policy decided.
	Rule string
	// Mode records how the decision was reached, for the audit log.
	Mode string
	// Reason is a short operator-facing explanation, used in the
	// X-Egress-Broker-Reason header on a refusal.
	Reason string
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
	var (
		intercept    *compiled
		hostOnlyDeny *compiled
		hostOnly     *compiled
		pathScoped   bool
	)

	for _, c := range e.rules {
		// A rule whose ports exclude this port simply does not apply. It is
		// not a refusal: the operator scoped it deliberately, and overriding
		// that would ignore what they wrote.
		if !c.host.Match(host) || !c.admitsPort(port) {
			continue
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
		return ConnectResult{Action: ConnectMITM, Rule: intercept.rule.Name, Mode: string(ModeIntercept)}
	}

	// Step 3. Deny first, so the outcome does not depend on rule order.
	if hostOnlyDeny != nil {
		return ConnectResult{
			Action: ConnectDeny,
			Rule:   hostOnlyDeny.rule.Name,
			Mode:   string(ModeDeny),
			Reason: "denied by rule " + hostOnlyDeny.rule.Name,
		}
	}
	if hostOnly != nil {
		return ConnectResult{Action: ConnectTunnel, Rule: hostOnly.rule.Name, Mode: string(ModeTunnel)}
	}

	// Step 4.
	if pathScoped {
		return ConnectResult{Action: ConnectMITM, Mode: ModeImplicitAllow}
	}

	// Step 5.
	return e.fallthroughDecision(port)
}

// fallthroughDecision applies the unmatched-host policy.
//
// Tunnelling defaults to port 443 here too. Without that, "fallthrough":
// "tunnel" would make the proxy a general-purpose TCP relay for every port an
// agent cares to name, which is a much larger grant than the operator asked
// for when they chose to relay unmatched HTTPS.
func (e *Engine) fallthroughDecision(port int) ConnectResult {
	if e.fallthrough_ == FallthroughDeny {
		return ConnectResult{
			Action: ConnectDeny,
			Mode:   ModeFallthrough,
			Reason: "no rule matches this host and fallthrough is \"deny\"",
		}
	}
	if port != DefaultTunnelPort {
		return ConnectResult{
			Action: ConnectDeny,
			Mode:   ModeFallthrough,
			Reason: "fallthrough tunnelling is limited to port 443; add a rule with an explicit \"ports\" list to allow this port",
		}
	}
	return ConnectResult{Action: ConnectTunnel, Mode: ModeFallthrough}
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
func (e *Engine) Evaluate(host string, port int, method, path string) Decision {
	var intercept *compiled

	for _, c := range e.rules {
		if !c.matchesRequest(host, port, method, path) {
			continue
		}
		if c.rule.Mode == ModeDeny {
			return Decision{
				Action: RequestDeny,
				Rule:   c.rule.Name,
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
			Mode:   string(ModeIntercept),
			Inject: intercept.rule.Inject,
		}
	}

	return Decision{Action: RequestForward, Mode: ModeImplicitAllow}
}

// matchesRequest reports whether the rule applies to this request.
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
