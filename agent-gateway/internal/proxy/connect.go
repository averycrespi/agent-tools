package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"golang.org/x/net/http2"
)

// serveConn handles a single inbound TCP connection. It reads the initial
// HTTP CONNECT request, authenticates the agent (when a registry is
// configured), makes the tunnel-vs-MITM decision, and dispatches accordingly.
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

	// connectTarget is the full "host:port" from the CONNECT request.
	connectTarget := req.Host

	// hostOnly is the bare hostname without port, used for TLS SNI and leaf
	// certificate issuance. If SplitHostPort fails the target has no port
	// (unusual) so use it as-is.
	hostOnly, _, splitErr := net.SplitHostPort(connectTarget)
	if splitErr != nil {
		hostOnly = connectTarget
	}

	// --- Authentication + intercept decision ---
	if p.registry != nil {
		authedAgent, authErr := Authenticate(context.Background(), p.registry, req.Header)
		if authErr != nil {
			_ = write407(conn)
			return
		}

		decision := Decide(context.Background(), connectTarget, authedAgent, p.rules, p.noInterceptHosts)
		switch decision {
		case DecisionReject:
			_ = write407(conn)
			return
		case DecisionTunnel:
			if err := writeResponse(conn, http.StatusOK, "Connection Established"); err != nil {
				return
			}
			p.serveTunnel(conn, br, connectTarget, authedAgent.Name)
			return
		default: // DecisionMITM
			if err := writeResponse(conn, http.StatusOK, "Connection Established"); err != nil {
				return
			}
			p.serveMITM(conn, br, connectTarget, hostOnly)
			return
		}
	}

	// No registry configured: legacy path — always MITM without auth.
	if err := writeResponse(conn, http.StatusOK, "Connection Established"); err != nil {
		return
	}
	p.serveMITM(conn, br, connectTarget, hostOnly)
}

// serveMITM performs TLS interception on conn. br is the buffered reader
// wrapping conn (may contain already-read bytes). connectTarget is the full
// host:port; hostOnly is the bare hostname for leaf-cert issuance.
func (p *Proxy) serveMITM(conn net.Conn, br *bufio.Reader, connectTarget, hostOnly string) {
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

// countingWriter wraps an io.Writer and counts total bytes written.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// serveTunnel relays raw TCP traffic bidirectionally between conn and the
// upstream address named in connectTarget. It dials connectTarget directly and
// pipes data in both directions until either side closes.
//
// agentName is the authenticated agent name (may be empty when no registry is
// configured). connectTarget is the full "host:port" from the CONNECT line.
func (p *Proxy) serveTunnel(conn net.Conn, br *bufio.Reader, connectTarget, agentName string) {
	reqID := NewULID()
	start := time.Now()

	// hostOnly is the bare hostname for the audit Host field.
	hostOnly, _, splitErr := net.SplitHostPort(connectTarget)
	if splitErr != nil {
		hostOnly = connectTarget
	}

	// Dial upstream first; if it fails we record a "blocked" audit entry.
	upstream, dialErr := net.Dial("tcp", connectTarget)
	if dialErr != nil {
		p.log.Debug("proxy: tunnel dial failed", "target", connectTarget, "err", dialErr)
		if p.auditor != nil {
			agentPtr := agentNamePtr(agentName)
			entry := audit.Entry{
				ID:           reqID,
				TS:           start,
				Agent:        agentPtr,
				Interception: "tunnel",
				Host:         hostOnly,
				DurationMS:   time.Since(start).Milliseconds(),
				Outcome:      "blocked",
			}
			if err := p.auditor.Record(context.Background(), entry); err != nil {
				p.log.Warn("proxy: audit record failed", "request_id", reqID, "err", err)
			}
		}
		return
	}
	defer func() { _ = upstream.Close() }()

	// Byte counters for both directions.
	// bytesOut = client → upstream (data the agent is sending out).
	// bytesIn  = upstream → client (data the agent is receiving).
	cwUp := &countingWriter{w: upstream} // client → upstream
	cwDown := &countingWriter{w: conn}   // upstream → client

	// If the bufio.Reader buffered bytes beyond the CONNECT line, drain them
	// first into the upstream connection before starting the bidirectional copy.
	if br.Buffered() > 0 {
		pre := make([]byte, br.Buffered())
		_, _ = io.ReadFull(br, pre)
		if _, err := upstream.Write(pre); err != nil {
			return
		}
		cwUp.n += int64(len(pre))
	}

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(cwUp, conn)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(cwDown, upstream)
		done <- struct{}{}
	}()
	<-done
	// Close both ends so the other goroutine unblocks.
	_ = conn.Close()
	_ = upstream.Close()
	<-done

	if p.auditor != nil {
		agentPtr := agentNamePtr(agentName)
		entry := audit.Entry{
			ID:           reqID,
			TS:           start,
			Agent:        agentPtr,
			Interception: "tunnel",
			Host:         hostOnly,
			DurationMS:   time.Since(start).Milliseconds(),
			BytesOut:     cwUp.n,
			BytesIn:      cwDown.n,
			Outcome:      "forwarded",
		}
		if err := p.auditor.Record(context.Background(), entry); err != nil {
			p.log.Warn("proxy: audit record failed", "request_id", reqID, "err", err)
		}
	}
}

// agentNamePtr returns a *string for agentName, or nil if empty.
func agentNamePtr(agentName string) *string {
	if agentName == "" {
		return nil
	}
	return &agentName
}

// writeResponse writes a minimal HTTP/1.1 response on conn.
func writeResponse(conn net.Conn, code int, msg string) error {
	line := fmt.Sprintf("HTTP/1.1 %d %s\r\n\r\n", code, msg)
	_, err := io.WriteString(conn, line)
	return err
}

// write407 writes a 407 Proxy Authentication Required response including the
// mandatory Proxy-Authenticate challenge header.
func write407(conn net.Conn) error {
	const resp = "HTTP/1.1 407 Proxy Authentication Required\r\n" +
		"Proxy-Authenticate: Basic realm=\"agent-gateway\"\r\n" +
		"\r\n"
	_, err := io.WriteString(conn, resp)
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
