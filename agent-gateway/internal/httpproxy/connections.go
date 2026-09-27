package httpproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/http2"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

type boundListener struct {
	net.Listener
	engine *Engine
}

func (l *boundListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		l.engine.mu.Lock()
		if l.engine.draining || len(l.engine.connections) >= contract.HTTPProxyConnections {
			l.engine.mu.Unlock()
			_ = c.Close()
			continue
		}
		conn := &boundConn{Conn: c, engine: l.engine, done: make(chan struct{})}
		l.engine.connections[conn] = struct{}{}
		l.engine.mu.Unlock()
		return conn, nil
	}
}

type boundConn struct {
	net.Conn
	engine *Engine
	once   sync.Once
	done   chan struct{}
}

func (c *boundConn) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return c.Close()
}

func (c *boundConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.engine.mu.Lock(); delete(c.engine.connections, c); c.engine.mu.Unlock(); close(c.done) })
	return err
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

type singleListener struct {
	conn     net.Conn
	done     <-chan struct{}
	accepted bool
}

func (l *singleListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *singleListener) Close() error   { return l.conn.Close() }
func (l *singleListener) Addr() net.Addr { return l.conn.LocalAddr() }

func (e *Engine) connect(w http.ResponseWriter, r *http.Request, lease *authorization.Lease, bearer string) {
	destination, err := httppolicy.ParseConnect(r.RequestURI, r.Host)
	if err != nil {
		e.rejectInvalid(w, r, lease, nil, "target", "invalid_connect_target")
		return
	}
	address, err := e.options.Remote.ResolveProxy(r.Context(), destination, e.options.Listeners)
	if err != nil {
		reject(w, http.StatusForbidden)
		return
	}
	identity, err := e.options.Evidence.PrepareIdentity()
	if err != nil {
		reject(w, http.StatusServiceUnavailable)
		return
	}
	result, err := e.options.Admissions.AdmitHTTP(r.Context(), lease, identity, authorization.HTTPAccessInput{PrincipalID: lease.Binding().PrincipalID, Connect: &contract.HTTPDestinationSelector{Host: destination.Host(), Port: destination.Port()}}, address.Facts(), e.options.Materials)
	if err != nil || !result.Committed || result.Evidence.Decision == nil {
		reject(w, http.StatusForbidden)
		return
	}
	decision := result.Evidence.Decision
	if decision.Transport == contract.HTTPTransportTunnel && result.DispatchAuthorized {
		e.mu.Lock()
		e.tunnels++
		e.mu.Unlock()
		defer func() { e.mu.Lock(); e.tunnels--; e.mu.Unlock() }()
		completion := contract.HTTPTrafficCompletion{Outcome: "prestart_failure"}
		defer func() { e.complete(result, identity, completion) }()
		admittedAt, parseErr := time.Parse(time.RFC3339Nano, identity.AdmittedAt)
		remaining := admittedAt.Add(contract.HTTPProxyTunnelLifetime).Sub(e.options.Now())
		if parseErr != nil || remaining <= 0 {
			reject(w, http.StatusGatewayTimeout)
			return
		}
		completion.Outcome = "outcome_unknown"
		dialCtx, cancelDial := context.WithTimeout(r.Context(), remaining)
		upstream, err := address.Dial(dialCtx, decision.PrivateGrant != nil)
		cancelDial()
		if err != nil {
			reject(w, http.StatusBadGateway)
			return
		}
		defer func() { _ = upstream.Close() }()
		client, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = client.Close() }()
		remaining = admittedAt.Add(contract.HTTPProxyTunnelLifetime).Sub(e.options.Now())
		if remaining <= 0 {
			return
		}
		stop := e.afterFunc(remaining, func() { _ = client.Close(); _ = upstream.Close() })
		defer stop()
		if err = writeConnected(client, buffered); err != nil {
			return
		}
		sent, received, complete := tunnel(client, buffered.Reader, upstream)
		completion.BytesSent = sent
		completion.BytesReceived = received
		if complete {
			completion.Outcome = "succeeded"
		}
		return
	}
	if decision.Transport != contract.HTTPTransportIntercept || decision.Reason != contract.HTTPReasonIntercept || e.options.Signer == nil {
		reject(w, http.StatusForbidden)
		return
	}
	certificate, err := e.options.Signer.Certificate(destination.Host())
	if err != nil {
		reject(w, http.StatusBadGateway)
		return
	}
	client, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	if err = writeConnected(client, buffered); err != nil {
		return
	}
	tlsConn := tls.Server(&bufferedConn{Conn: client, reader: buffered.Reader}, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}, GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if err := checkSNI(destination, hello); err != nil {
			return nil, err
		}
		return certificate, nil
	}}) //nolint:gosec // TLS 1.2 remains supported for proxy clients.
	ctx, cancel := context.WithTimeout(r.Context(), contract.HTTPProxyDialTimeout)
	err = tlsConn.HandshakeContext(ctx)
	cancel()
	if err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	state := tlsConn.ConnectionState()
	inside := &intercepted{destination: destination, bearer: bearer, binding: lease.Binding(), sni: state.ServerName, connect: contract.HTTPConnectContext{ID: identity.InvocationID, Host: destination.Host(), Port: destination.Port()}}
	// CONNECT authentication is not retained as a pending lease or inner-stream
	// entitlement. Every stream uses the original bearer and pins its identity.
	lease.Release()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { e.handle(w, r, inside) })
	if state.NegotiatedProtocol == "h2" {
		server := &http2.Server{MaxConcurrentStreams: contract.HTTPProxyH2Streams, MaxReadFrameSize: 16 * 1024, IdleTimeout: contract.HTTPProxyIdleTimeout, ReadIdleTimeout: contract.HTTPProxyIdleTimeout, PingTimeout: contract.HTTPProxyDialTimeout, WriteByteTimeout: contract.HTTPProxyIdleTimeout, MaxUploadBufferPerConnection: contract.HTTPProxyBufferBytes * contract.HTTPProxyH2Streams, MaxUploadBufferPerStream: contract.HTTPProxyBufferBytes}
		server.ServeConn(newH2BoundConn(tlsConn), &http2.ServeConnOpts{Context: r.Context(), Handler: handler, BaseConfig: &http.Server{MaxHeaderBytes: contract.HTTPProxyHeaderBytes, ReadHeaderTimeout: contract.HTTPProxyHeaderTimeout, ErrorLog: diagnostics.HTTPErrorLog()}})
		return
	}
	bound, ok := client.(*boundConn)
	if !ok {
		return
	}
	server := e.newServer(handler)
	_ = server.Serve(&singleListener{conn: tlsConn, done: bound.done})
}

func writeConnected(client net.Conn, b *bufio.ReadWriter) error {
	if err := client.SetWriteDeadline(time.Now().Add(contract.HTTPProxyHeaderTimeout)); err != nil {
		return err
	}
	if _, err := b.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return err
	}
	if err := b.Flush(); err != nil {
		return err
	}
	return client.SetDeadline(time.Time{})
}

func tunnel(client net.Conn, reader io.Reader, upstream net.Conn) (int64, int64, bool) {
	type copied struct {
		sent bool
		n    int64
		err  error
	}
	results := make(chan copied, 2)
	copySide := func(sent bool, destination net.Conn, writer io.Writer, source io.Reader) {
		n, err := io.CopyBuffer(writer, source, make([]byte, contract.HTTPProxyBufferBytes))
		if err == nil {
			if half, ok := destination.(interface{ CloseWrite() error }); ok {
				err = half.CloseWrite()
			} else {
				err = destination.Close()
			}
		}
		results <- copied{sent, n, err}
	}
	go copySide(true, upstream, upstream, &idleTunnelReader{client, reader})
	go copySide(false, client, &idleTunnelWriter{client}, upstream)
	var sent, received int64
	complete := true
	for range 2 {
		result := <-results
		if result.sent {
			sent = result.n
		} else {
			received = result.n
		}
		if result.err != nil {
			complete = false
			_ = client.Close()
			_ = upstream.Close()
		}
	}
	return sent, received, complete
}

type idleTunnelReader struct {
	conn   net.Conn
	reader io.Reader
}

func (r *idleTunnelReader) Read(p []byte) (int, error) {
	if err := r.conn.SetReadDeadline(time.Now().Add(contract.HTTPProxyIdleTimeout)); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type idleTunnelWriter struct{ net.Conn }

func (w *idleTunnelWriter) Write(p []byte) (int, error) {
	if err := w.SetWriteDeadline(time.Now().Add(contract.HTTPProxyIdleTimeout)); err != nil {
		return 0, err
	}
	return w.Conn.Write(p)
}
