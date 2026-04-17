package proxy

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/http2"
)

// serveConn handles a single inbound TCP connection. It reads the initial
// HTTP request, dispatches CONNECT to the MITM path, and rejects anything
// else with 400.
func (p *Proxy) serveConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		// Unreadable request — close silently.
		return
	}

	if req.Method != http.MethodConnect {
		// Only CONNECT is supported in v1; plain HTTP is deferred.
		_ = writeResponse(conn, http.StatusBadRequest, "only CONNECT supported")
		return
	}

	// TODO(Task 24): parse Proxy-Authorization and authenticate the agent.
	// For this milestone any (or no) Proxy-Authorization is accepted.
	// _ = req.Header.Get("Proxy-Authorization")

	// TODO(Task 24): consult no_intercept_hosts and per-agent rule set to
	// decide tunnel vs MITM. For this milestone we always MITM.

	// connectTarget is the full "host:port" from the CONNECT request (used
	// as the upstream URL host so the correct port is dialled).
	connectTarget := req.Host

	// hostOnly is the bare hostname without port, used for TLS SNI and leaf
	// certificate issuance. If SplitHostPort fails the target has no port
	// (unusual) so use it as-is.
	hostOnly, _, err := net.SplitHostPort(connectTarget)
	if err != nil {
		hostOnly = connectTarget
	}

	// Acknowledge the CONNECT.
	if err := writeResponse(conn, http.StatusOK, "Connection Established"); err != nil {
		return
	}

	// The bufio reader may have buffered bytes beyond the CONNECT request line.
	// For a well-behaved client the CONNECT body is empty so the reader buffer
	// should be empty here, but we drain it defensively.
	tlsBase := conn
	if br.Buffered() > 0 {
		tlsBase = &connWithBuffer{Conn: conn, r: io.MultiReader(br, conn)}
	}

	// Obtain the leaf TLS config for this host (bare hostname, no port).
	tlsCfg, err := p.ca.ServerConfig(hostOnly)
	if err != nil {
		p.log.Error("proxy: ServerConfig failed", "host", hostOnly, "err", err)
		return
	}

	// Wrap the connection in TLS and complete the handshake.
	// Set a deadline to prevent slow/malicious clients from blocking this
	// goroutine indefinitely (DoS mitigation).
	tlsConn := tls.Server(tlsBase, tlsCfg)
	if err := tlsBase.SetDeadline(time.Now().Add(p.handshakeTimeout)); err != nil {
		p.log.Debug("proxy: SetDeadline failed", "host", hostOnly, "err", err)
		return
	}
	if err := tlsConn.Handshake(); err != nil {
		p.log.Debug("proxy: TLS handshake failed", "host", hostOnly, "err", err)
		return
	}
	// Clear the deadline so normal request processing is not bounded.
	if err := tlsConn.SetDeadline(time.Time{}); err != nil {
		p.log.Debug("proxy: clear deadline failed", "host", hostOnly, "err", err)
		return
	}

	// Dispatch based on negotiated ALPN. Pass the full host:port target so
	// the upstream URL is constructed correctly.
	proto := tlsConn.ConnectionState().NegotiatedProtocol
	if proto == "h2" {
		p.serveH2(tlsConn, connectTarget)
	} else {
		p.serveH1(tlsConn, connectTarget)
	}
}

// writeResponse writes a minimal HTTP/1.1 response on conn.
func writeResponse(conn net.Conn, code int, msg string) error {
	line := fmt.Sprintf("HTTP/1.1 %d %s\r\n\r\n", code, msg)
	_, err := io.WriteString(conn, line)
	return err
}

// serveH2 serves the MITM'd connection using HTTP/2.
func (p *Proxy) serveH2(conn *tls.Conn, host string) {
	srv := &http2.Server{
		IdleTimeout: p.idleTimeout,
	}
	srv.ServeConn(conn, &http2.ServeConnOpts{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p.handle(w, r, host)
		}),
	})
}

// serveH1 serves the MITM'd connection using HTTP/1 by looping over requests.
func (p *Proxy) serveH1(conn *tls.Conn, host string) {
	// Wrap in a single-connection net.Listener so http.Server can drive the
	// loop (including keep-alive). This is simpler than hand-rolling the loop.
	ln := newSingleConnListener(conn)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p.handle(w, r, host)
		}),
		ReadHeaderTimeout: p.readHeaderTimeout, //nolint:gosec
	}
	// Serve returns when the connection closes; ignore the error.
	_ = srv.Serve(ln)
}

// connWithBuffer wraps a net.Conn but substitutes a custom Reader that drains
// any bytes already buffered in a bufio.Reader before reading from the wire.
type connWithBuffer struct {
	net.Conn
	r io.Reader
}

func (c *connWithBuffer) Read(b []byte) (int, error) { return c.r.Read(b) }

// Ensure connWithBuffer satisfies net.Conn at compile time.
var _ net.Conn = (*connWithBuffer)(nil)

// Ensure the deadline methods satisfy net.Conn (inherited from net.Conn embed).
var _ interface {
	SetDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
} = (*connWithBuffer)(nil)

// singleConnListener is a net.Listener that yields exactly one connection.
// After that connection is returned, Accept blocks until Close is called.
// Use newSingleConnListener to construct; do not create with a struct literal.
type singleConnListener struct {
	conn net.Conn
	ch   chan struct{}
}

// newSingleConnListener constructs a singleConnListener with ch eagerly
// initialized so that Accept and Close agree on its existence with no race.
func newSingleConnListener(conn net.Conn) *singleConnListener {
	return &singleConnListener{conn: conn, ch: make(chan struct{})}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	if l.conn != nil {
		c := l.conn
		l.conn = nil
		return c, nil
	}
	// Block until Close is called.
	<-l.ch
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error {
	close(l.ch)
	return nil
}

func (l *singleConnListener) Addr() net.Addr { return stubAddr{} }

// stubAddr is a minimal net.Addr for adapters that need one.
type stubAddr struct{}

func (stubAddr) Network() string { return "tcp" }
func (stubAddr) String() string  { return "stub" }
