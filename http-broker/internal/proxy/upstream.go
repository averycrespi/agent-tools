package proxy

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
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

// newUpstreamTransport builds the transport used for intercepted requests.
//
// D12: verification is on, the system trust store is used, and TLS 1.3 is the
// floor. Interception exists so the proxy can inspect and inject on this
// connection — it is not a licence to weaken the connection the proxy makes on
// the agent's behalf, which is the one that actually carries the credential.
//
// The dialer is netguard's, so an intercepted request is guarded on exactly
// the same terms as a tunnel (D8).
func (p *Proxy) newUpstreamTransport() *http.Transport {
	return &http.Transport{
		DialContext:           p.dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   upstreamTLSTimeout,
		ResponseHeaderTimeout: upstreamResponseTimeout,
		IdleConnTimeout:       upstreamIdleTimeout,
		MaxIdleConns:          upstreamMaxIdleConns,
		MaxIdleConnsPerHost:   upstreamMaxPerHost,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13,
			// InsecureSkipVerify is deliberately absent. An upstream whose
			// certificate does not verify is one this proxy must not carry a
			// credential to.
		},
	}
}

// BaseDialer is the net.Dialer netguard wraps for upstream connections. The
// composition root builds the guard around it so both proxy paths share one.
func BaseDialer() *net.Dialer {
	return &net.Dialer{Timeout: upstreamDialTimeout, KeepAlive: 30 * time.Second}
}
