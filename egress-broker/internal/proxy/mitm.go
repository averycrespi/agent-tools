package proxy

import (
	"net/http"
	"time"

	"github.com/averycrespi/agent-tools/egress-broker/internal/rules"
)

// serveMITM terminates TLS and evaluates each request on the connection.
//
// Not yet implemented: TLS termination lands with the certificate work and the
// request pipeline lands with injection. Until then this refuses rather than
// forwarding, so a rule that says "intercept" can never silently degrade into
// an unaudited, uninjected pass-through — which would look like the feature
// working while doing none of it.
func (p *Proxy) serveMITM(w http.ResponseWriter, _ *http.Request, host string, port int, decision rules.ConnectResult, start time.Time) {
	p.audit.Record(Event{
		Start: start, Interception: "mitm", Host: host, Port: port,
		Status: http.StatusNotImplemented, MatchedRule: decision.Rule, Mode: decision.Mode,
		Outcome: OutcomeError, Error: "interception not yet implemented",
		DurationMS: sinceMS(start),
	})
	p.refuse(w, http.StatusNotImplemented, ReasonUpstreamFailure,
		"TLS interception is not yet wired up in this build")
}

// handleAbsolute serves an absolute-form plain HTTP request through the same
// rules pipeline the MITM path uses.
//
// Not yet implemented, for the same reason as serveMITM: refusing is the only
// safe placeholder for a path whose whole purpose is to apply policy.
func (p *Proxy) handleAbsolute(w http.ResponseWriter, _ *http.Request) {
	p.refuse(w, http.StatusNotImplemented, ReasonUpstreamFailure,
		"plain-HTTP proxying is not yet wired up in this build")
}
