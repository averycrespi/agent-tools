package proxy_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/agents"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/proxy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- stub registry --------------------------------------------------------

// stubRegistry is a test double for agents.Registry that authenticates a
// single hard-coded token and rejects all others.
type stubRegistry struct {
	validToken string
	agent      *agents.Agent
}

func newStubRegistry(token string) *stubRegistry {
	return &stubRegistry{
		validToken: token,
		agent:      &agents.Agent{Name: "test-agent"},
	}
}

func (r *stubRegistry) Add(_ context.Context, _, _ string) (string, error) {
	panic("not implemented")
}

func (r *stubRegistry) Authenticate(_ context.Context, token string) (*agents.Agent, error) {
	if token == r.validToken {
		return r.agent, nil
	}
	return nil, agents.ErrInvalidToken
}

func (r *stubRegistry) Rotate(_ context.Context, _ string) (string, error) {
	panic("not implemented")
}

func (r *stubRegistry) Rm(_ context.Context, _ string) error {
	panic("not implemented")
}

func (r *stubRegistry) List(_ context.Context) ([]agents.AgentMetadata, error) {
	panic("not implemented")
}

func (r *stubRegistry) ReloadFromDB(_ context.Context) error {
	panic("not implemented")
}

// ---- stub decide engine ---------------------------------------------------

// decideStubEngine is a minimal RulesEngine that returns a fixed host set.
// Renamed to avoid conflict with stubRulesEngine in pipeline_test.go.
type decideStubEngine struct {
	hosts map[string]struct{}
}

func newDecideStubEngine(hosts ...string) *decideStubEngine {
	m := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		m[h] = struct{}{}
	}
	return &decideStubEngine{hosts: m}
}

func (e *decideStubEngine) Evaluate(_ *rules.Request) *rules.MatchResult { return nil }

func (e *decideStubEngine) HostsForAgent(_ string) map[string]struct{} {
	return e.hosts
}

// ---- helpers --------------------------------------------------------------

// makeProxyAuthHeader builds a valid Basic Proxy-Authorization header.
func makeProxyAuthHeader(token string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte("x:" + token))
	return "Basic " + encoded
}

const testToken = "agw_test_token_1234567890abcdef123456789"

// ---- parseAuth tests ------------------------------------------------------

func TestParseAuth_Valid(t *testing.T) {
	hdr := makeProxyAuthHeader(testToken)
	tok, ok := proxy.ParseAuthForTest(hdr)
	assert.True(t, ok)
	assert.Equal(t, testToken, tok)
}

func TestParseAuth_Empty(t *testing.T) {
	_, ok := proxy.ParseAuthForTest("")
	assert.False(t, ok)
}

func TestParseAuth_NonBasicScheme(t *testing.T) {
	_, ok := proxy.ParseAuthForTest("Bearer sometoken")
	assert.False(t, ok)
}

func TestParseAuth_MalformedBase64(t *testing.T) {
	_, ok := proxy.ParseAuthForTest("Basic !!!notbase64!!!")
	assert.False(t, ok)
}

func TestParseAuth_MissingColon(t *testing.T) {
	// Decoded value has no ":" separator.
	encoded := base64.StdEncoding.EncodeToString([]byte("nocolon"))
	_, ok := proxy.ParseAuthForTest("Basic " + encoded)
	assert.False(t, ok)
}

// ---- Authenticate tests ---------------------------------------------------

func TestAuthenticate_Valid(t *testing.T) {
	reg := newStubRegistry(testToken)
	hdr := http.Header{"Proxy-Authorization": []string{makeProxyAuthHeader(testToken)}}
	ag, err := proxy.AuthenticateForTest(context.Background(), reg, hdr)
	require.NoError(t, err)
	assert.Equal(t, "test-agent", ag.Name)
}

func TestAuthenticate_MissingHeader(t *testing.T) {
	reg := newStubRegistry(testToken)
	_, err := proxy.AuthenticateForTest(context.Background(), reg, http.Header{})
	assert.Error(t, err)
}

func TestAuthenticate_WrongToken(t *testing.T) {
	reg := newStubRegistry(testToken)
	hdr := http.Header{"Proxy-Authorization": []string{makeProxyAuthHeader("agw_wrong_token")}}
	_, err := proxy.AuthenticateForTest(context.Background(), reg, hdr)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, agents.ErrInvalidToken))
}

// ---- Decide tests (decision table) ----------------------------------------

// Row 2: token valid, host in no_intercept_hosts → tunnel
func TestDecide_NoInterceptHost_Tunnel(t *testing.T) {
	ag := &agents.Agent{Name: "test-agent"}
	engine := newDecideStubEngine("api.github.com")
	decision := proxy.DecideForTest(context.Background(), "cdn.example.com", ag, engine, []string{"cdn.example.com"})
	assert.Equal(t, proxy.DecisionTunnel, decision)
}

// Row 3: token valid, host NOT in no_intercept_hosts, no rule matches → tunnel
func TestDecide_NoRuleMatch_Tunnel(t *testing.T) {
	ag := &agents.Agent{Name: "test-agent"}
	engine := newDecideStubEngine("api.github.com")
	decision := proxy.DecideForTest(context.Background(), "other.example.com", ag, engine, nil)
	assert.Equal(t, proxy.DecisionTunnel, decision)
}

// Row 4: token valid, rule matches, IP literal → tunnel
func TestDecide_IPLiteralV4_Tunnel(t *testing.T) {
	ag := &agents.Agent{Name: "test-agent"}
	engine := newDecideStubEngine("192.168.1.1")
	decision := proxy.DecideForTest(context.Background(), "192.168.1.1", ag, engine, nil)
	assert.Equal(t, proxy.DecisionTunnel, decision)
}

// Row 4 (IPv6): token valid, rule matches, IPv6 literal → tunnel
func TestDecide_IPLiteralV6_Tunnel(t *testing.T) {
	ag := &agents.Agent{Name: "test-agent"}
	engine := newDecideStubEngine("::1")
	decision := proxy.DecideForTest(context.Background(), "::1", ag, engine, nil)
	assert.Equal(t, proxy.DecisionTunnel, decision)
}

// Row 5: token valid, rule matches, not IP → MITM
func TestDecide_RuleMatchHostname_MITM(t *testing.T) {
	ag := &agents.Agent{Name: "test-agent"}
	engine := newDecideStubEngine("api.github.com")
	decision := proxy.DecideForTest(context.Background(), "api.github.com", ag, engine, nil)
	assert.Equal(t, proxy.DecisionMITM, decision)
}

// Row 5 (glob): rule matches via glob pattern → MITM
func TestDecide_GlobRuleMatch_MITM(t *testing.T) {
	ag := &agents.Agent{Name: "test-agent"}
	engine := newDecideStubEngine("*.github.com")
	decision := proxy.DecideForTest(context.Background(), "api.github.com", ag, engine, nil)
	assert.Equal(t, proxy.DecisionMITM, decision)
}

// no_intercept_hosts: glob matching — *.example.com matches sub.example.com
func TestDecide_NoInterceptGlob_Tunnel(t *testing.T) {
	ag := &agents.Agent{Name: "test-agent"}
	engine := newDecideStubEngine("sub.example.com")
	decision := proxy.DecideForTest(context.Background(), "sub.example.com", ag, engine, []string{"*.example.com"})
	assert.Equal(t, proxy.DecisionTunnel, decision)
}

// no_intercept_hosts: *.example.com does NOT match a.b.example.com (single-label glob)
func TestDecide_NoInterceptGlobSingleLabel(t *testing.T) {
	ag := &agents.Agent{Name: "test-agent"}
	engine := newDecideStubEngine("a.b.example.com")
	decision := proxy.DecideForTest(context.Background(), "a.b.example.com", ag, engine, []string{"*.example.com"})
	assert.Equal(t, proxy.DecisionMITM, decision)
}

// no_intercept_hosts: **.example.com matches a.b.example.com (multi-label glob)
func TestDecide_NoInterceptDoubleGlob_Tunnel(t *testing.T) {
	ag := &agents.Agent{Name: "test-agent"}
	engine := newDecideStubEngine("a.b.example.com")
	decision := proxy.DecideForTest(context.Background(), "a.b.example.com", ag, engine, []string{"**.example.com"})
	assert.Equal(t, proxy.DecisionTunnel, decision)
}

// ---- CONNECT handler tests (407 path) ------------------------------------

// TestCONNECT_NoAuthHeader checks that a missing Proxy-Authorization returns 407.
func TestCONNECT_NoAuthHeader(t *testing.T) {
	auth := newTestAuthority(t)
	reg := newStubRegistry(testToken)
	p := proxy.New(proxy.Deps{
		CA:                   auth,
		Registry:             reg,
		UpstreamRoundTripper: roundTripperFunc(testEchoHandler),
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go p.Serve(ln)
	t.Cleanup(func() { _ = ln.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	_, err = io.WriteString(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	require.NoError(t, err)

	buf := make([]byte, 512)
	n, _ := conn.Read(buf)
	assert.Contains(t, string(buf[:n]), "407")
}

// TestCONNECT_InvalidToken checks that a bad token returns 407.
func TestCONNECT_InvalidToken(t *testing.T) {
	auth := newTestAuthority(t)
	reg := newStubRegistry(testToken)
	p := proxy.New(proxy.Deps{
		CA:                   auth,
		Registry:             reg,
		UpstreamRoundTripper: roundTripperFunc(testEchoHandler),
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go p.Serve(ln)
	t.Cleanup(func() { _ = ln.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	badAuth := makeProxyAuthHeader("agw_bad_token")
	_, err = io.WriteString(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: "+badAuth+"\r\n\r\n")
	require.NoError(t, err)

	buf := make([]byte, 512)
	n, _ := conn.Read(buf)
	assert.Contains(t, string(buf[:n]), "407")
}

// TestCONNECT_MalformedAuth checks that a malformed auth header returns 407.
func TestCONNECT_MalformedAuth(t *testing.T) {
	auth := newTestAuthority(t)
	reg := newStubRegistry(testToken)
	p := proxy.New(proxy.Deps{
		CA:                   auth,
		Registry:             reg,
		UpstreamRoundTripper: roundTripperFunc(testEchoHandler),
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go p.Serve(ln)
	t.Cleanup(func() { _ = ln.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	_, err = io.WriteString(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: NotBasic !!!bad!!!\r\n\r\n")
	require.NoError(t, err)

	buf := make([]byte, 512)
	n, _ := conn.Read(buf)
	assert.Contains(t, string(buf[:n]), "407")
}

// TestCONNECT_ValidAuth_MITM verifies that a valid token with a host matching
// a rule returns 200 Connection Established and then the proxy performs TLS MITM.
// The test uses raw TCP + a manual TLS client to avoid any transport-level
// header stripping that some http.Transport versions apply to Proxy-Authorization.
func TestCONNECT_ValidAuth_MITM(t *testing.T) {
	auth := newTestAuthority(t)
	reg := newStubRegistry(testToken)
	// Provide a rules engine that matches "example.com" for this agent so that
	// Decide returns DecisionMITM (rather than DecisionTunnel due to no-rule).
	engine := newDecideStubEngine("example.com")
	p := proxy.New(proxy.Deps{
		CA:                   auth,
		Registry:             reg,
		Rules:                engine,
		UpstreamRoundTripper: roundTripperFunc(testEchoHandler),
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go p.Serve(ln)
	t.Cleanup(func() { _ = ln.Close() })

	// Connect directly to the proxy and send CONNECT with a valid auth header.
	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	authHdr := makeProxyAuthHeader(testToken)
	_, err = io.WriteString(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: "+authHdr+"\r\n\r\n")
	require.NoError(t, err)

	// Read and verify the 200 Connection Established response.
	// Use raw string matching on the first read to avoid blocking on body reads.
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	response := string(buf[:n])
	require.Contains(t, response, "200", "expected 200 Connection Established for authenticated MITM path")

	// Verify TLS handshake succeeds with the proxy's leaf cert (MITM).
	// The 200 response is "HTTP/1.1 200 Connection Established\r\n\r\n" with no
	// trailing bytes, so conn has no pre-buffered TLS data.
	rootPool := x509.NewCertPool()
	rootPool.AppendCertsFromPEM(auth.RootPEM())

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName: "example.com",
		RootCAs:    rootPool,
	})
	err = tlsConn.Handshake()
	require.NoError(t, err, "TLS handshake should succeed — proxy presents a leaf cert signed by test CA")
	_ = tlsConn.Close()
}

// TestCONNECT_ValidAuth_Tunnel verifies that a host in no_intercept_hosts is
// tunnelled (raw pipe) rather than MITM'd. We use an actual TCP upstream echo
// server to confirm data passes through unchanged.
func TestCONNECT_ValidAuth_Tunnel(t *testing.T) {
	// Start a simple TCP echo server as the upstream.
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	echoAddr := echoLn.Addr().String()
	echoHost, echoPort, _ := net.SplitHostPort(echoAddr)

	go func() {
		for {
			conn, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				buf := make([]byte, 256)
				n, _ := c.Read(buf)
				_, _ = c.Write(buf[:n])
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = echoLn.Close() })

	auth := newTestAuthority(t)
	reg := newStubRegistry(testToken)
	noIntercept := []string{echoHost}
	p := proxy.New(proxy.Deps{
		CA:                   auth,
		Registry:             reg,
		NoInterceptHosts:     noIntercept,
		UpstreamRoundTripper: roundTripperFunc(testEchoHandler),
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go p.Serve(ln)
	t.Cleanup(func() { _ = ln.Close() })

	// Connect directly to the proxy and send a CONNECT request.
	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	target := echoHost + ":" + echoPort
	authHdr := makeProxyAuthHeader(testToken)
	connectReq := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\nProxy-Authorization: " + authHdr + "\r\n\r\n"
	_, err = io.WriteString(conn, connectReq)
	require.NoError(t, err)

	// Read the 200 Connection Established response.
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	require.Contains(t, string(buf[:n]), "200", "expected 200 Connection Established for tunnelled host")

	// Send data through the tunnel — the echo server must reflect it back.
	_, err = io.WriteString(conn, "HELLO_TUNNEL")
	require.NoError(t, err)

	got := make([]byte, 12)
	_, err = io.ReadFull(conn, got)
	require.NoError(t, err)
	assert.Equal(t, "HELLO_TUNNEL", string(got))
}
