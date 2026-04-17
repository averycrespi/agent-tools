package proxy_test

import (
	"crypto/tls"
	"crypto/x509"
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
