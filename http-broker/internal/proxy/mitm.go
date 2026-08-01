package proxy

import (
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/http2"

	"github.com/averycrespi/agent-tools/http-broker/internal/rules"
)

// HTTP/2 server limits for the interception endpoint.
//
// The reference implementation this design draws on cited CVE-2023-44487
// (Rapid Reset) and CVE-2024-27316 (CONTINUATION flood). Both are cheap for a
// client to trigger and expensive for a server to absorb, and the client here
// is an agent we are explicitly not trusting to behave.
const (
	h2MaxConcurrentStreams = 100
	h2MaxFrameSize         = 1 << 14 // 16 KiB, the RFC 7540 default
	h2IdleTimeout          = 5 * time.Minute
	// MaxUploadBufferPerStream bounds how much a single stream may buffer,
	// which is the other half of limiting what a burst of streams can hold.
	h2MaxUploadBufferPerStream = 1 << 20 // 1 MiB
	// h2MaxHeaderBytes bounds one request's header block, which is the
	// CONTINUATION-flood limit. It reaches the h2 server through
	// ServeConnOpts.BaseConfig; http2.Server has no field of its own for it,
	// and MaxDecoderHeaderTableSize — which does sit on http2.Server — is the
	// HPACK compression table, not a header-list bound. Setting that to a
	// megabyte advertised a 256x larger table to an untrusted client, the
	// opposite of hardening.
	h2MaxHeaderBytes = 1 << 20 // 1 MiB
)

// serveMITM terminates TLS with a leaf bound to the CONNECT host, then serves
// requests on that connection through the rules pipeline.
//
// D11: the leaf is issued for the CONNECT hostname, not the SNI the client
// sends. Trusting SNI would let a client request a certificate for one name
// while the policy decision was made for another.
func (p *Proxy) serveMITM(w http.ResponseWriter, r *http.Request, id, host string, port int, engine *rules.Engine, decision rules.ConnectResult, start time.Time) {
	tlsCfg, err := p.authority.ServerConfig(host)
	if err != nil {
		p.audit.Record(Event{
			ID: id, Start: start, Interception: "mitm", Host: host, Port: port,
			Status: http.StatusInternalServerError, MatchedRule: decision.Rule, Mode: decision.Mode,
			Outcome: OutcomeError, Error: "issuing a leaf certificate failed", DurationMS: sinceMS(start),
		})
		p.refuse(w, http.StatusInternalServerError, ReasonUpstreamFailure, "could not issue a certificate for this host")
		return
	}

	client, err := hijack(w)
	if err != nil {
		p.log.Error("hijacking the client connection failed", "error", err)
		return
	}
	defer func() { _ = client.Close() }()

	p.hijacked.Add(1)
	defer p.hijacked.Done()

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		return
	}

	tlsConn := tls.Server(&idleConn{Conn: client, idle: p.tunnelIdle}, tlsCfg)
	if err := tlsConn.HandshakeContext(r.Context()); err != nil {
		// A pinning client fails here by design. The README documents the
		// signature and the mode:"tunnel" fix, because the error the agent
		// sees is otherwise indistinguishable from a network fault.
		p.log.Debug("TLS handshake with the client failed",
			"host", host, "port", port, "error", err)
		p.audit.Record(Event{
			ID: id, Start: start, Interception: "mitm", Host: host, Port: port,
			MatchedRule: decision.Rule, Mode: decision.Mode, Outcome: OutcomeError,
			Error:      "client TLS handshake failed (a certificate-pinning client needs a mode:\"tunnel\" rule)",
			DurationMS: sinceMS(start),
		})
		return
	}
	defer func() { _ = tlsConn.Close() }()

	handler := &mitmHandler{proxy: p, host: host, port: port, engine: engine}

	if tlsConn.ConnectionState().NegotiatedProtocol == "h2" {
		p.serveH2(tlsConn, handler)
		return
	}
	p.serveH1(tlsConn, handler)
}

// serveH2 serves an intercepted connection that negotiated HTTP/2.
func (p *Proxy) serveH2(conn net.Conn, handler http.Handler) {
	server := &http2.Server{
		MaxConcurrentStreams:     h2MaxConcurrentStreams,
		MaxReadFrameSize:         h2MaxFrameSize,
		MaxUploadBufferPerStream: h2MaxUploadBufferPerStream,
		IdleTimeout:              h2IdleTimeout,
	}
	// BaseConfig is where the header-block limit comes from; leaving it nil
	// left the CONTINUATION-flood bound unset entirely.
	server.ServeConn(conn, &http2.ServeConnOpts{
		Handler:    handler,
		BaseConfig: &http.Server{MaxHeaderBytes: h2MaxHeaderBytes, ReadHeaderTimeout: p.headerTimeout},
	})
}

// serveH1 serves an intercepted connection over HTTP/1.1.
//
// http.Server drives keep-alive, pipelining and chunked bodies correctly, so
// the connection is handed to one rather than parsing requests by hand.
//
// The listener is closed from a ConnState hook once the connection reaches a
// terminal state, which is what lets Serve return. Without that, Accept blocks
// forever after handing over its one connection, Serve never returns, and this
// frame never unwinds — pinning a goroutine and leaving serveMITM's deferred
// Close calls unexecuted for the life of the process. That is one leaked
// goroutine and one leaked TLS connection per intercepted HTTP/1.1
// connection, which an agent can produce at will.
func (p *Proxy) serveH1(conn net.Conn, handler http.Handler) {
	listener := newSingleConnListener(conn)

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: p.headerTimeout,
		IdleTimeout:       h2IdleTimeout,
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed || state == http.StateHijacked {
				_ = listener.Close()
			}
		},
	}

	// Serve returns once the listener is closed, which happens above when the
	// connection finishes. Errors here are the expected "use of closed
	// listener".
	_ = server.Serve(listener)
}

// singleConnListener hands one already-accepted connection to an http.Server,
// then blocks in Accept until Close is called.
type singleConnListener struct {
	conn     net.Conn
	accepted sync.Once
	ch       chan net.Conn
	done     chan struct{}
	closeOne sync.Once
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	return &singleConnListener{
		conn: conn,
		ch:   make(chan net.Conn, 1),
		done: make(chan struct{}),
	}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	l.accepted.Do(func() { l.ch <- l.conn })

	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *singleConnListener) Close() error {
	l.closeOne.Do(func() { close(l.done) })
	return nil
}

func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// mitmHandler serves requests on one intercepted connection.
//
// It carries the CONNECT host and port, which are authoritative: D10 says the
// CONNECT target wins over any Host header inside the tunnel, because the
// policy decision was made against the former and a client must not be able to
// redirect it by writing a different Host.
//
// It also carries the rules snapshot the CONNECT decision was made against,
// which is Store.Engine()'s documented contract: take it once per connection.
// Re-taking it per request let a SIGHUP land mid-connection and leave the
// CONNECT decision and the per-request decisions evaluating different
// rulesets.
type mitmHandler struct {
	proxy  *Proxy
	host   string
	port   int
	engine *rules.Engine
}

func (h *mitmHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.proxy.handleIntercepted(w, r, h.host, h.port, "https", h.engine)
}
