//go:build integration

package httpproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

type fixtureClock struct{}

func (fixtureClock) Now() time.Time { return time.Now().UTC() }

type memoryBackend struct {
	mu    sync.Mutex
	items map[string]string
}

func (*memoryBackend) Probe(context.Context, string) error { return nil }
func (m *memoryBackend) Set(s, u, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[s+u] = v
	return nil
}
func (m *memoryBackend) Get(s, u string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.items[s+u]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}
func (m *memoryBackend) Delete(s, u string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, s+u)
	return nil
}

type proxyFixture struct {
	engine     *Engine
	address    string
	credential contract.AgentCredentialCreation
	traffic    *invocation.TrafficStore
	authority  *authorization.Repository
	materials  *httpcredentials.Service
	roots      *x509.CertPool
	backend    *memoryBackend
}

func fixture(t *testing.T) *proxyFixture {
	t.Helper()
	ctx := audit.WithSystem(t.Context())
	const installation = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	owner, err := gatewaypaths.Acquire(filepath.Join(t.TempDir(), "gateway"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	store, err := storage.Initialize(ctx, owner, installation)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	clock := fixtureClock{}
	authority, err := authorization.New(store, clock, rand.Reader)
	require.NoError(t, err)
	principal, err := authority.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "HTTP fixture", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	credential, err := authority.IssueCredential(ctx, principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	backend := &memoryBackend{items: map[string]string{}}
	provider, err := keyring.NewProviderWithBackend(installation, backend)
	require.NoError(t, err)
	coordinator := keyring.NewCoordinator(provider, store, clock, rand.Reader)
	materialRepo, err := httpcredentials.NewRepository(store, clock, rand.Reader, authority)
	require.NoError(t, err)
	materials, err := httpcredentials.NewService(materialRepo, coordinator, installation)
	require.NoError(t, err)
	ca, err := httpca.New(store, coordinator, installation, clock, rand.Reader)
	require.NoError(t, err)
	t.Cleanup(ca.Close)
	require.NoError(t, ca.Replace(ctx, "0"))
	signer, err := ca.Load(ctx)
	require.NoError(t, err)
	public, _, err := ca.PublicCertificate(ctx)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(public))
	config := invocation.DefaultTrafficConfig()
	config.BudgetBytes = 8 << 20
	traffic, err := invocation.CreateTraffic(ctx, owner, installation, "01ARZ3NDEKTSV4RRFFQ69G5FAW", config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, traffic.Close()) })
	evidence, err := invocation.NewTrafficRepository(traffic, clock, rand.Reader, func(contract.Invalidation) {})
	require.NoError(t, err)
	admissions, err := invocation.NewAdmissionCoordinator(evidence, authority)
	require.NoError(t, err)
	engine, err := New(Options{Authority: authority, Evidence: evidence, Admissions: admissions, Materials: materials, Remote: remote.New(remote.Options{}), Signer: signer, Listeners: func() []netip.AddrPort { return nil }, Now: clock.Now})
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- engine.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, engine.Close(ctx))
		select {
		case err := <-done:
			require.ErrorIs(t, err, http.ErrServerClosed)
		case <-ctx.Done():
			t.Error("proxy serve failed to join")
		}
	})
	return &proxyFixture{engine: engine, address: listener.Addr().String(), credential: credential, traffic: traffic, authority: authority, materials: materials, roots: roots, backend: backend}
}
func (f *proxyFixture) allow(t *testing.T, raw, kind, path, credential string) {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	addr, err := netip.ParseAddrPort(u.Host)
	require.NoError(t, err)
	var policy contract.HTTPPolicy
	policy.Version = 1
	policy.Type = contract.HTTPGrantType(kind)
	private := true
	policy.AllowPrivate = &private
	if kind == "allow_tunnel" {
		policy.Destination = &contract.HTTPDestinationSelector{Host: addr.Addr().String(), Port: addr.Port()}
	} else {
		policy.Request = &contract.HTTPRequestSelector{Origin: contract.HTTPOriginSelector{Scheme: u.Scheme, Host: addr.Addr().String(), Port: addr.Port()}, Methods: contract.HTTPMethods{Any: true}, Path: contract.HTTPPathSelector{Kind: contract.HTTPPathAny}}
		if path != "" {
			policy.Request.Path = contract.HTTPPathSelector{Kind: contract.HTTPPathPrefix, Value: path}
		}
		if credential != "" {
			policy.CredentialID = &credential
		}
	}
	encoded, err := json.Marshal(policy)
	require.NoError(t, err)
	_, err = f.authority.PutHTTPGrant(audit.WithSystem(t.Context()), "", "", authorization.HTTPGrantInput{PrincipalID: f.credential.Principal.ID, Policy: encoded})
	require.NoError(t, err)
}
func (f *proxyFixture) client(t *testing.T) *http.Client {
	t.Helper()
	proxy, err := url.Parse("http://" + f.address)
	require.NoError(t, err)
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (f *proxyFixture) request(t *testing.T, client *http.Client, method, raw string, body io.Reader) *http.Response {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), method, raw, body)
	require.NoError(t, err)
	r.Header.Set("Proxy-Authorization", "Bearer "+f.credential.Bearer)
	response, err := client.Do(r)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}
func (f *proxyFixture) intercept(t *testing.T, upstream, alpn string) *tls.Conn {
	t.Helper()
	u, err := url.Parse(upstream)
	require.NoError(t, err)
	conn, err := net.DialTimeout("tcp", f.address, time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Bearer %s\r\n\r\n", u.Host, u.Host, f.credential.Bearer)
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	tlsConn := tls.Client(&bufferedConn{Conn: conn, reader: reader}, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.roots, ServerName: u.Hostname(), NextProtos: []string{alpn}})
	require.NoError(t, tlsConn.HandshakeContext(t.Context()))
	require.NoError(t, conn.SetDeadline(time.Time{}))
	return tlsConn
}

func TestIntegrationPlainStreamingAdmissionAndNoProxySecret(t *testing.T) {
	f := fixture(t)
	var calls atomic.Int64
	ordering := make(chan bool, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		history, err := f.traffic.HTTPHistory(r.Context(), 0, 10)
		ordering <- err == nil && len(history.Records) == 1 && history.Records[0].Admission.Decision.Allowed
		if r.Header.Get("Proxy-Authorization") != "" {
			w.WriteHeader(500)
			return
		}
		_, _ = io.Copy(w, r.Body)
	}))
	defer upstream.Close()
	f.allow(t, upstream.URL, "allow_requests", "", "")
	payload := bytes.Repeat([]byte("bounded-stream-private-canary"), 100000)
	response := f.request(t, f.client(t), "POST", upstream.URL+"/upload?private=query", bytes.NewReader(payload))
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	require.Equal(t, payload, body)
	require.True(t, <-ordering)
	require.EqualValues(t, 1, calls.Load())
}

func TestIntegrationInterceptH1H2FreshAuthorizationAndInjection(t *testing.T) {
	for _, protocol := range []string{"http/1.1", "h2"} {
		t.Run(protocol, func(t *testing.T) {
			f := fixture(t)
			var calls atomic.Int64
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer injected-canary" || r.Header.Get("Proxy-Authorization") != "" {
					w.WriteHeader(500)
					return
				}
				_, _ = io.WriteString(w, "ok")
			}))
			defer upstream.Close()
			f.engine.roots = x509.NewCertPool()
			f.engine.roots.AddCert(upstream.Certificate())
			u, err := url.Parse(upstream.URL)
			require.NoError(t, err)
			addr, err := netip.ParseAddrPort(u.Host)
			require.NoError(t, err)
			material, err := f.materials.Create(audit.WithSystem(t.Context()), httpcredentials.Definition{Name: "fixture", Boundary: httpcredentials.Boundary{Host: addr.Addr().String(), Port: addr.Port()}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte("injected-canary"))
			require.NoError(t, err)
			f.allow(t, upstream.URL, "allow_requests", "", material.ID)
			conn := f.intercept(t, upstream.URL, protocol)
			var perform func() (*http.Response, error)
			if protocol == "h2" {
				transport := &http2.Transport{}
				cc, err := transport.NewClientConn(conn)
				require.NoError(t, err)
				defer func() { _ = cc.Close() }()
				perform = func() (*http.Response, error) {
					req, err := http.NewRequestWithContext(t.Context(), "GET", upstream.URL+"/", nil)
					if err != nil {
						return nil, err
					}
					req.Header.Set("Authorization", "client-value")
					return cc.RoundTrip(req)
				}
			} else {
				reader := bufio.NewReader(conn)
				perform = func() (*http.Response, error) {
					_, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nAuthorization: client-value\r\n\r\n", u.Host)
					if err != nil {
						return nil, err
					}
					return http.ReadResponse(reader, &http.Request{Method: "GET"})
				}
			}
			response, err := perform()
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, "ok", string(body))
			_, err = f.authority.RevokeCredential(audit.WithSystem(t.Context()), f.credential.Principal.ID, f.credential.Principal.Revision)
			require.NoError(t, err)
			response, err = perform()
			require.NoError(t, err)
			require.Equal(t, http.StatusProxyAuthRequired, response.StatusCode)
			_ = response.Body.Close()
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestIntegrationDeniedPathsAndAmbiguousFramingNeverDispatch(t *testing.T) {
	f := fixture(t)
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer upstream.Close()
	f.allow(t, upstream.URL, "allow_requests", "/approved", "")
	client := f.client(t)
	for _, path := range []string{"/approved-sibling", "/approved/%2fsecret", "/approved/../other"} {
		response := f.request(t, client, "GET", upstream.URL+path, nil)
		require.GreaterOrEqual(t, response.StatusCode, 400)
		_ = response.Body.Close()
	}
	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	conn, err := net.DialTimeout("tcp", f.address, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
	_, err = fmt.Fprintf(conn, "POST %s/approved HTTP/1.1\r\nHost: %s\r\nHost: attacker.invalid\r\nProxy-Authorization: Bearer %s\r\nContent-Length: 1\r\n\r\nx", upstream.URL, u.Host, f.credential.Bearer)
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "POST"})
	require.NoError(t, err)
	require.Equal(t, 400, response.StatusCode)
	_ = response.Body.Close()
	require.Zero(t, calls.Load())
}

func TestIntegrationOpaqueTunnelHardExpiry(t *testing.T) {
	f := fixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.Copy(c, c)
	}()
	f.allow(t, "http://"+listener.Addr().String(), "allow_tunnel", "", "")
	fired := make(chan func(), 1)
	f.engine.afterFunc = func(d time.Duration, fn func()) func() {
		if d <= 0 || d > time.Hour {
			panic("invalid lifetime")
		}
		fired <- fn
		return func() {}
	}
	conn, err := net.DialTimeout("tcp", f.address, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Bearer %s\r\n\r\n", listener.Addr(), listener.Addr(), f.credential.Bearer)
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	_, err = io.WriteString(conn, "opaque")
	require.NoError(t, err)
	received := make([]byte, 6)
	_, err = io.ReadFull(reader, received)
	require.NoError(t, err)
	require.Equal(t, "opaque", string(received))
	(<-fired)()
	_, err = reader.ReadByte()
	require.Error(t, err)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tunnel did not clean up")
	}
}
