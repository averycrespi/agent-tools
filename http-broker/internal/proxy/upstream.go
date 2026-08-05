package proxy

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"github.com/averycrespi/agent-tools/http-broker/internal/rules"
)

// Upstream transport tuning. These bound how much state one misbehaving
// upstream can hold open, and are deliberately modest: the client population
// is a handful of sandboxes, not the internet.
const (
	upstreamDialTimeout     = 15 * time.Second
	upstreamTLSTimeout      = 15 * time.Second
	upstreamResponseTimeout = 60 * time.Second
	upstreamIdleTimeout     = 90 * time.Second
	upstreamMaxIdleConns    = 100
	upstreamMaxPerHost      = 10
)

// supportedTLSFloors are the floors a rule may select, and so the transports
// the pool holds. It mirrors internal/rules' accepted min_tls_version values;
// anything else resolves to the default.
var supportedTLSFloors = []uint16{tls.VersionTLS12, tls.VersionTLS13}

// newUpstreamTransports builds one transport per supported TLS floor.
//
// One per floor rather than one per request because tls.Config lives on the
// transport, not the request: a floor cannot ride the request context the way
// netguard's allow_private grant does. Sharing a transport per floor also keeps
// connection pooling intact, which a per-request transport would destroy.
//
// D12: verification is on and the system trust store is used for every floor.
// A rule may lower which TLS versions are negotiated; nothing lets it stop the
// certificate from being verified. Interception exists so the proxy can inspect
// and inject on this connection — it is not a licence to weaken the connection
// the proxy makes on the agent's behalf, which is the one that actually carries
// the credential.
//
// The dialer is netguard's, so an intercepted request is guarded on exactly
// the same terms as a tunnel (D8).
func (p *Proxy) newUpstreamTransports() map[uint16]*http.Transport {
	out := make(map[uint16]*http.Transport, len(supportedTLSFloors))
	for _, floor := range supportedTLSFloors {
		out[floor] = &http.Transport{
			DialContext:           p.dialer.DialContext,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   upstreamTLSTimeout,
			ResponseHeaderTimeout: upstreamResponseTimeout,
			IdleConnTimeout:       upstreamIdleTimeout,
			MaxIdleConns:          upstreamMaxIdleConns,
			MaxIdleConnsPerHost:   upstreamMaxPerHost,
			TLSClientConfig: &tls.Config{
				MinVersion: floor,
				// InsecureSkipVerify is deliberately absent, at every floor. An
				// upstream whose certificate does not verify is one this proxy
				// must not carry a credential to.
			},
		}
	}
	return out
}

// transportFor returns the transport enforcing floor.
//
// An unrecognised floor gets the default rather than an error: the caller is on
// the request path, and failing closed at the strictest floor is the safe
// direction. Load-time validation is what rejects a bad value, so reaching here
// with one would be a bug, not operator input.
func (p *Proxy) transportFor(floor uint16) *http.Transport {
	if tr, ok := p.transports[floor]; ok {
		return tr
	}
	return p.transports[rules.DefaultMinTLSVersion]
}

// BaseDialer is the net.Dialer netguard wraps for upstream connections. The
// composition root builds the guard around it so both proxy paths share one.
func BaseDialer() *net.Dialer {
	return &net.Dialer{Timeout: upstreamDialTimeout, KeepAlive: 30 * time.Second}
}
