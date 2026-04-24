package proxy_test

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/ca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roundTripperFunc adapts a function to the http.RoundTripper interface.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// testEchoHandler is a fake upstream that always returns 200 with body "echo".
func testEchoHandler(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("echo")),
		Request:    r,
	}, nil
}

// newTestAuthority creates an Authority with a freshly generated root CA.
func newTestAuthority(t *testing.T) *ca.Authority {
	t.Helper()
	dir := t.TempDir()
	a, err := ca.LoadOrGenerate(
		filepath.Join(dir, "ca.key"),
		filepath.Join(dir, "ca.pem"),
	)
	require.NoError(t, err)
	return a
}

func TestCONNECT_ParsesAndHandshakes(t *testing.T) {
	auth := newTestAuthority(t)
	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: roundTripperFunc(testEchoHandler),
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go p.Serve(ln)
	t.Cleanup(func() { _ = ln.Close() })

	// Build a root pool trusting our test CA.
	rootPool := x509.NewCertPool()
	rootPool.AppendCertsFromPEM(auth.RootPEM())

	// Configure a transport that uses our proxy and trusts the test CA.
	proxyURL := &url.URL{
		Scheme: "http",
		Host:   ln.Addr().String(),
	}
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{
			RootCAs: rootPool,
		},
	}
	client := &http.Client{Transport: transport}

	// Make a request through the proxy — the CONNECT target is "example.com:443"
	// but it will be intercepted (MITM'd) by the proxy.
	resp, err := client.Get("https://example.com/hello")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "echo", string(body))
}

// TestCONNECT_HandshakeTimeout verifies that when a client sends CONNECT but
// never completes the TLS handshake, the proxy goroutine exits within a short
// window rather than blocking indefinitely.
func TestCONNECT_HandshakeTimeout(t *testing.T) {
	auth := newTestAuthority(t)
	const handshakeTimeout = 50 * time.Millisecond
	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: roundTripperFunc(testEchoHandler),
		HandshakeTimeout:     handshakeTimeout,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go p.Serve(ln)
	t.Cleanup(func() { _ = ln.Close() })

	// Connect and send a valid CONNECT request.
	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	_, err = io.WriteString(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	require.NoError(t, err)

	// Read and discard the 200 Connection Established response.
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	require.Contains(t, string(buf[:n]), "200")

	// Now deliberately stall — never send TLS ClientHello.
	// The proxy should close the connection once handshakeTimeout elapses.
	// We give it 5× headroom to avoid flakiness on slow CI.
	deadline := time.Now().Add(5 * handshakeTimeout)
	_ = conn.SetReadDeadline(deadline)

	// A read should return an error (EOF or reset) once the proxy closes.
	_, readErr := conn.Read(buf)
	assert.Error(t, readErr, "expected proxy to close connection after handshake timeout")
	assert.WithinDuration(t, time.Now(), deadline, 5*handshakeTimeout,
		"proxy should close connection well before our outer deadline")
}

// TestCONNECT_PreservesUpstreamPort is a regression test for the port-handling
// bug: when the CONNECT target is host:port with port != 443, both
// req.URL.Host and req.Host forwarded to the upstream must include the port.
//
// The test fails on pre-fix code that stripped or ignored the port, and passes
// with the current fix.
func TestCONNECT_PreservesUpstreamPort(t *testing.T) {
	// Spin up a real TLS server on an ephemeral port to use as the "upstream"
	// address the client will CONNECT to. We don't need the server to accept
	// real connections; we only need a valid port that is != 443.
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	upstreamPort := upstream.Addr().(*net.TCPAddr).Port
	_ = upstream.Close()
	target := fmt.Sprintf("127.0.0.1:%d", upstreamPort)

	// Fake RoundTripper that records URL.Host and Host from the forwarded request.
	type recorded struct {
		urlHost string
		host    string
	}
	ch := make(chan recorded, 1)
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		ch <- recorded{urlHost: r.URL.Host, host: r.Host}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    r,
		}, nil
	})

	auth := newTestAuthority(t)
	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: rt,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go p.Serve(ln)
	t.Cleanup(func() { _ = ln.Close() })

	// Build a root pool trusting our test CA.
	rootPool := x509.NewCertPool()
	rootPool.AppendCertsFromPEM(auth.RootPEM())

	// Configure a transport that routes through our proxy and trusts the test CA.
	proxyURL := &url.URL{Scheme: "http", Host: ln.Addr().String()}
	transport := &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: rootPool},
	}
	client := &http.Client{Transport: transport}

	// Issue GET https://127.0.0.1:<ephemeral-port>/whatever.
	// The client will send CONNECT 127.0.0.1:<port> HTTP/1.1, which is != 443.
	resp, err := client.Get("https://" + target + "/whatever")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Retrieve what the fake upstream received.
	var rec recorded
	select {
	case rec = <-ch:
	default:
		t.Fatal("fake RoundTripper was never called")
	}

	assert.Equal(t, target, rec.urlHost, "req.URL.Host must preserve port")
	assert.Equal(t, target, rec.host, "req.Host must preserve port")
}

func TestCONNECT_NonCONNECT_Returns400(t *testing.T) {
	auth := newTestAuthority(t)
	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: roundTripperFunc(testEchoHandler),
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go p.Serve(ln)
	t.Cleanup(func() { _ = ln.Close() })

	// Connect directly and send a plain GET (not CONNECT).
	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	_, err = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	require.NoError(t, err)

	// Read the status line back.
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	status := string(buf[:n])
	assert.Contains(t, status, "400")
}

// TestServeConn_MalformedHostRejected verifies that a CONNECT with a host that
// cannot be normalised (invalid punycode) is rejected with 400 and no upstream
// dial is attempted. Fail-open here would let an IDN homograph bypass rules
// by falling through to tunnel mode with mangled audit data.
func TestServeConn_MalformedHostRejected(t *testing.T) {
	auth := newTestAuthority(t)
	reg := newStubRegistry(testToken)

	// Record any upstream round-trip attempts. The MITM path routes through
	// UpstreamRoundTripper; the tunnel path would dial TCP directly. For this
	// test the CONNECT must be rejected before either path is reached, so the
	// counter must remain zero.
	var rtCalls int
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		rtCalls++
		return testEchoHandler(r)
	})

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		Registry:             reg,
		UpstreamRoundTripper: rt,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go p.Serve(ln)
	t.Cleanup(func() { _ = ln.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// xn--p-ecp.ru is an invalid-punycode label that hostnorm.Normalize
	// rejects. The wire format is still parseable by http.ReadRequest.
	authHdr := makeProxyAuthHeader(testToken)
	_, err = io.WriteString(conn, "CONNECT xn--p-ecp.ru:443 HTTP/1.1\r\nHost: xn--p-ecp.ru:443\r\nProxy-Authorization: "+authHdr+"\r\n\r\n")
	require.NoError(t, err)

	buf := make([]byte, 512)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := conn.Read(buf)
	assert.Contains(t, string(buf[:n]), "400",
		"expected 400 Bad Request for un-normalizable CONNECT target")
	assert.NotContains(t, string(buf[:n]), "200",
		"must not return 200 Connection Established for malformed host")

	// Give the proxy goroutine a moment to finish; then assert no upstream
	// RoundTrip was attempted.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, rtCalls, "proxy must not dial upstream on malformed host")
}

// TestConnect_TunnelAudits verifies §5 row 1: the tunnel path writes an audit
// entry with Interception="tunnel", nil Method, nil Path, and a non-zero
// BytesIn + BytesOut when data flows through the tunnel.
func TestConnect_TunnelAudits(t *testing.T) {
	auth := newTestAuthority(t)
	cl := newCapturingLogger(t)

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: roundTripperFunc(testEchoHandler),
		Auditor:              cl,
	})

	// Spin up a real TCP upstream that echoes everything it receives.
	upLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = upLn.Close() })

	go func() {
		conn, aErr := upLn.Accept()
		if aErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.Copy(conn, conn) // echo
	}()

	target := upLn.Addr().String()

	// Build a pair of connected net.Conns to simulate the client side.
	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()

	// Run serveTunnel in a goroutine; it blocks until both sides close.
	const agentName = "test-agent"
	done := make(chan struct{})
	go func() {
		defer close(done)
		br := bufio.NewReader(bytes.NewReader(nil)) // empty pre-read buffer
		p.ServeTunnelForTest(serverConn, br, target, agentName)
	}()

	// Send some bytes through the tunnel and then close the client side.
	msg := []byte("hello tunnel")
	_, err = clientConn.Write(msg)
	require.NoError(t, err)

	// Read the echo back (the upstream echoes what we sent).
	readBuf := make([]byte, len(msg))
	_, err = io.ReadFull(clientConn, readBuf)
	require.NoError(t, err)
	assert.Equal(t, msg, readBuf)

	// Close the client side to unblock serveTunnel.
	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveTunnel did not finish within timeout")
	}

	// Assert the audit entry was recorded.
	require.NotNil(t, cl.last, "audit entry must be recorded for tunnel")
	e := cl.last

	assert.Equal(t, "tunnel", e.Interception)
	assert.Nil(t, e.Method, "tunnel rows must have nil Method")
	assert.Nil(t, e.Path, "tunnel rows must have nil Path")
	assert.Equal(t, "forwarded", e.Outcome)
	require.NotNil(t, e.Agent)
	assert.Equal(t, agentName, *e.Agent)
	assert.Greater(t, e.BytesOut, int64(0), "BytesOut must be > 0 when data was sent")
	assert.Greater(t, e.BytesIn, int64(0), "BytesIn must be > 0 when echo was received")
	assert.NotEmpty(t, e.ID, "audit entry must have a request ID")
}

// TestServeH2_LimitsConfigured verifies that the MITM'd HTTP/2 server has the
// hardening limits set by the proxy: these caps defend against Rapid Reset
// (CVE-2023-44487) and CONTINUATION flood (CVE-2024-27316). The stdlib default
// values target public-internet servers tuned for maximum concurrency; a
// sandbox proxy handles one agent at a time, so tighter caps are safer.
func TestServeH2_LimitsConfigured(t *testing.T) {
	auth := newTestAuthority(t)
	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: roundTripperFunc(testEchoHandler),
	})

	srv := p.NewH2ServerForTest()
	require.NotNil(t, srv)
	assert.Equal(t, uint32(100), srv.MaxConcurrentStreams,
		"MaxConcurrentStreams must be capped to mitigate CVE-2023-44487 (Rapid Reset)")
	assert.Equal(t, uint32(16<<10), srv.MaxReadFrameSize,
		"MaxReadFrameSize must be capped for a sandbox proxy")
	assert.Equal(t, uint32(4096), srv.MaxDecoderHeaderTableSize,
		"MaxDecoderHeaderTableSize must be capped to mitigate CVE-2024-27316 (CONTINUATION flood)")
	assert.Equal(t, uint32(4096), srv.MaxEncoderHeaderTableSize,
		"MaxEncoderHeaderTableSize must be capped to mitigate CVE-2024-27316 (CONTINUATION flood)")
}

// TestServeH1_LimitsConfigured verifies that the MITM'd HTTP/1 server has the
// hardening limits set by the proxy. MaxHeaderBytes defends against oversized
// header floods; the stdlib default (1 MiB) is intended for a public-internet
// server, not a sandbox proxy serving a single agent.
func TestServeH1_LimitsConfigured(t *testing.T) {
	auth := newTestAuthority(t)
	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: roundTripperFunc(testEchoHandler),
	})

	srv := p.NewH1ServerForTest()
	require.NotNil(t, srv)
	assert.Equal(t, 64<<10, srv.MaxHeaderBytes,
		"MaxHeaderBytes must be capped (64 KiB) to resist oversized-header floods")
	assert.NotZero(t, srv.ReadHeaderTimeout,
		"ReadHeaderTimeout must be configured to resist Slowloris")
	assert.NotZero(t, srv.IdleTimeout,
		"IdleTimeout must be configured so idle connections are reclaimed")
}

// TestConnect_TunnelAudits_DialFail verifies that when the tunnel dial fails,
// a "blocked" audit entry is still recorded.
func TestConnect_TunnelAudits_DialFail(t *testing.T) {
	auth := newTestAuthority(t)
	cl := newCapturingLogger(t)

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: roundTripperFunc(testEchoHandler),
		Auditor:              cl,
	})

	// Use a port that is not listening so the dial will fail immediately.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closedTarget := ln.Addr().String()
	_ = ln.Close() // close immediately so dial fails

	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	br := bufio.NewReader(bytes.NewReader(nil))
	p.ServeTunnelForTest(serverConn, br, closedTarget, "test-agent")

	require.NotNil(t, cl.last, "audit entry must be recorded even on dial failure")
	assert.Equal(t, "tunnel", cl.last.Interception)
	assert.Equal(t, "blocked", cl.last.Outcome)
	assert.Nil(t, cl.last.Method)
	assert.Nil(t, cl.last.Path)
}
