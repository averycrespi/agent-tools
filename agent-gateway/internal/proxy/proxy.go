// Package proxy implements the HTTP/HTTPS MITM proxy for agent-gateway.
// Connections arrive as plain HTTP CONNECT requests; the proxy terminates TLS
// on behalf of the agent using a dynamically-issued leaf certificate from the
// local CA, then forwards decoded requests to the configured upstream
// RoundTripper.
package proxy

import (
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const (
	defaultHandshakeTimeout  = 10 * time.Second
	defaultReadHeaderTimeout = 10 * time.Second
	defaultIdleTimeout       = 90 * time.Second
)

// CA is the interface required by Proxy for TLS interception.
// It is satisfied by *ca.Authority but kept as an interface so tests can
// substitute a lightweight fake without importing the ca package.
//
// See also the internal/ca Authority interface definition in §13 of the design.
type CA interface {
	// ServerConfig returns a *tls.Config with a leaf certificate for host,
	// signed by the root CA. Multiple calls for the same host return the
	// same config pointer (cached).
	ServerConfig(host string) (*tls.Config, error)
}

// Deps groups all injectable dependencies for a Proxy.
type Deps struct {
	// CA provides leaf TLS configs for MITM interception. Required.
	CA CA

	// UpstreamRoundTripper is used to forward decoded requests to the real
	// upstream server. If nil, a default http.Transport is used (set during
	// Serve). Inject a fake in tests.
	UpstreamRoundTripper http.RoundTripper

	// Logger is the structured logger. If nil, the default slog logger is used.
	Logger *slog.Logger

	// HandshakeTimeout is the maximum time allowed for a TLS handshake with a
	// MITM'd client. Zero uses the default of 10s.
	HandshakeTimeout time.Duration

	// ReadHeaderTimeout is the maximum time the embedded http.Server waits to
	// read HTTP/1 request headers (guards against Slowloris). Zero uses the
	// default of 10s.
	ReadHeaderTimeout time.Duration

	// IdleTimeout is the maximum time an idle HTTP/2 server connection may
	// remain open. Zero uses the default of 90s.
	IdleTimeout time.Duration
}

// Proxy is the MITM proxy. Create one with New and call Serve to accept
// connections.
type Proxy struct {
	ca                CA
	rt                http.RoundTripper
	log               *slog.Logger
	handshakeTimeout  time.Duration
	readHeaderTimeout time.Duration
	idleTimeout       time.Duration
}

// New constructs a Proxy from the given Deps. It panics if Deps.CA is nil.
func New(d Deps) *Proxy {
	if d.CA == nil {
		panic("proxy: Deps.CA must not be nil")
	}
	rt := d.UpstreamRoundTripper
	if rt == nil {
		rt = &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig: &tls.Config{
				// Use the system trust store — we are strict TLS clients to
				// upstream. Verification failures become 502.
				InsecureSkipVerify: false, //nolint:gosec
			},
		}
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	ht := d.HandshakeTimeout
	if ht == 0 {
		ht = defaultHandshakeTimeout
	}
	rht := d.ReadHeaderTimeout
	if rht == 0 {
		rht = defaultReadHeaderTimeout
	}
	it := d.IdleTimeout
	if it == 0 {
		it = defaultIdleTimeout
	}
	return &Proxy{
		ca:                d.CA,
		rt:                rt,
		log:               log,
		handshakeTimeout:  ht,
		readHeaderTimeout: rht,
		idleTimeout:       it,
	}
}

// Serve accepts connections on ln and dispatches each to a goroutine. It
// returns when ln.Close() is called (or ln.Accept returns a permanent error).
func (p *Proxy) Serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			// net.Listener.Close() causes Accept to return a non-nil error.
			// Any accept error terminates the loop.
			return
		}
		go p.serveConn(conn)
	}
}
