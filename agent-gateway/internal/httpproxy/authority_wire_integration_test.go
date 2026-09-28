//go:build integration

package httpproxy

import (
	"bufio"
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
)

func TestIntegrationOuterTargetAuthorityOverridesRawHost(t *testing.T) {
	for _, protocol := range []string{"http", "http/1.1", "h2"} {
		for _, mode := range []string{"different-credentials", "raw-host-blocked", "target-blocked", "target-no-private"} {
			t.Run(protocol+"/"+mode, func(t *testing.T) {
				f := fixture(t)
				type observation struct{ host, target, authorization, proxy string }
				received := make(chan observation, 1)
				var targetCalls, otherCalls atomic.Int64
				start := func(handler http.HandlerFunc) *httptest.Server {
					if protocol == "http" {
						return httptest.NewServer(handler)
					}
					return httptest.NewTLSServer(handler)
				}
				target := start(func(w http.ResponseWriter, r *http.Request) {
					targetCalls.Add(1)
					received <- observation{r.Host, r.RequestURI, r.Header.Get("Authorization"), r.Header.Get("Proxy-Authorization")}
					w.WriteHeader(204)
				})
				defer target.Close()
				other := start(func(w http.ResponseWriter, _ *http.Request) { otherCalls.Add(1); w.WriteHeader(204) })
				defer other.Close()
				targetURL, err := url.Parse(target.URL)
				require.NoError(t, err)
				otherURL, err := url.Parse(other.URL)
				require.NoError(t, err)
				if protocol != "http" {
					f.engine.roots = x509.NewCertPool()
					f.engine.roots.AddCert(target.Certificate())
					f.engine.roots.AddCert(other.Certificate())
				}
				put := func(u *url.URL, kind contract.HTTPGrantType, private bool, secret string) string {
					addr, err := netip.ParseAddrPort(u.Host)
					require.NoError(t, err)
					p := contract.HTTPPolicy{Version: 1, Type: kind, Request: &contract.HTTPRequestSelector{Origin: contract.HTTPOriginSelector{Scheme: u.Scheme, Host: addr.Addr().String(), Port: addr.Port()}, Methods: contract.HTTPMethods{Any: true}, Path: contract.HTTPPathSelector{Kind: contract.HTTPPathAny}}}
					if kind == contract.HTTPAllowRequests {
						p.AllowPrivate = &private
					}
					credentialID := ""
					if kind == contract.HTTPAllowRequests && protocol != "http" {
						material, err := f.materials.Create(audit.WithSystem(t.Context()), httpcredentials.Definition{Name: secret, Boundary: httpcredentials.Boundary{Host: addr.Addr().String(), Port: addr.Port()}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte(secret))
						require.NoError(t, err)
						credentialID = material.ID
						p.CredentialID = &credentialID
					}
					raw, err := json.Marshal(p)
					require.NoError(t, err)
					_, err = f.authority.PutHTTPGrant(audit.WithSystem(t.Context()), "", "", authorization.HTTPGrantInput{PrincipalID: f.credential.Principal.ID, Policy: raw})
					require.NoError(t, err)
					return credentialID
				}
				targetCredential := put(targetURL, contract.HTTPAllowRequests, mode != "target-no-private", "target-fixture-secret")
				put(otherURL, contract.HTTPAllowRequests, true, "other-fixture-secret")
				if mode == "target-blocked" {
					put(targetURL, contract.HTTPBlockRequests, false, "")
				}
				if mode == "raw-host-blocked" {
					put(otherURL, contract.HTTPBlockRequests, false, "")
				}
				conn, err := net.DialTimeout("tcp", f.address, time.Second)
				require.NoError(t, err)
				defer func() { _ = conn.Close() }()
				require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
				const path = "/https://example.com//%2f?x=1&x=2+3"
				var response *http.Response
				if protocol == "http" {
					_, err = fmt.Fprintf(conn, "GET %s%s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Bearer %s\r\nConnection: close\r\n\r\n", target.URL, path, otherURL.Host, f.credential.Bearer)
					require.NoError(t, err)
					response, err = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "GET"})
				} else {
					// The conflicting outer Host must not select the other origin's grant,
					// private permission or credential. Inner authority still must agree.
					_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Bearer %s\r\n\r\n", targetURL.Host, otherURL.Host, f.credential.Bearer)
					require.NoError(t, err)
					reader := bufio.NewReader(conn)
					connected, e := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
					require.NoError(t, e)
					require.Equal(t, 200, connected.StatusCode)
					secure := tls.Client(&bufferedConn{Conn: conn, reader: reader}, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.roots, ServerName: targetURL.Hostname(), NextProtos: []string{protocol}})
					require.NoError(t, secure.HandshakeContext(t.Context()))
					if protocol == "h2" {
						cc, e := (&http2.Transport{}).NewClientConn(secure)
						require.NoError(t, e)
						defer func() { _ = cc.Close() }()
						req, e := http.NewRequestWithContext(t.Context(), "GET", target.URL+path, nil)
						require.NoError(t, e)
						response, err = cc.RoundTrip(req)
					} else {
						_, err = fmt.Fprintf(secure, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, targetURL.Host)
						require.NoError(t, err)
						response, err = http.ReadResponse(bufio.NewReader(secure), &http.Request{Method: "GET"})
					}
				}
				require.NoError(t, err)
				_, err = io.Copy(io.Discard, response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				allowed := mode != "target-blocked" && mode != "target-no-private"
				require.Zero(t, otherCalls.Load())
				if !allowed {
					require.Equal(t, 403, response.StatusCode)
					require.Zero(t, targetCalls.Load())
					return
				}
				require.Equal(t, 204, response.StatusCode)
				require.EqualValues(t, 1, targetCalls.Load())
				wantCredential := ""
				if protocol != "http" {
					wantCredential = "Bearer target-fixture-secret"
				}
				select {
				case got := <-received:
					require.Equal(t, observation{targetURL.Host, path, wantCredential, ""}, got)
				case <-time.After(time.Second):
					t.Fatal("missing authoritative target capture")
				}
				history, err := f.traffic.HTTPHistory(t.Context(), 0, 10)
				require.NoError(t, err)
				found := false
				for _, record := range history.Records {
					a := record.Admission
					if a.Target != nil && a.Target.Method == "GET" {
						found = true
						require.True(t, a.Decision.Allowed)
						require.Equal(t, targetURL.Hostname(), a.Target.Host)
						require.Equal(t, netip.MustParseAddrPort(targetURL.Host).Port(), a.Target.Port)
						if targetCredential != "" {
							require.Equal(t, targetCredential, a.Decision.Credential.ID)
						} else {
							require.Nil(t, a.Decision.Credential)
						}
					}
				}
				require.True(t, found)
			})
		}
	}
}

func TestIntegrationInterceptedAuthorityDisagreementNeverDispatches(t *testing.T) {
	f := fixture(t)
	var calls atomic.Int64
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer upstream.Close()
	f.allow(t, upstream.URL, "allow_requests", "", "")
	for _, protocol := range []string{"http/1.1", "h2"} {
		t.Run(protocol, func(t *testing.T) {
			conn := f.intercept(t, upstream.URL, protocol)
			require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
			defer func() { _ = conn.Close() }()
			var response *http.Response
			var err error
			if protocol == "h2" {
				cc, e := (&http2.Transport{}).NewClientConn(conn)
				require.NoError(t, e)
				defer func() { _ = cc.Close() }()
				req, e := http.NewRequestWithContext(t.Context(), "GET", upstream.URL+"/a//%2f", nil)
				require.NoError(t, e)
				req.Host = "other.example.com"
				response, err = cc.RoundTrip(req)
			} else {
				_, err = fmt.Fprintf(conn, "GET /a//%%2f HTTP/1.1\r\nHost: other.example.com\r\nConnection: close\r\n\r\n")
				require.NoError(t, err)
				response, err = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "GET"})
			}
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			require.Equal(t, 400, response.StatusCode)
			require.Zero(t, calls.Load())
		})
	}
}

func TestIntegrationSNIDisagreementNeverDispatches(t *testing.T) {
	f := fixture(t)
	var calls atomic.Int64
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer upstream.Close()
	f.allow(t, upstream.URL, "allow_requests", "", "")
	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	conn, err := net.DialTimeout("tcp", f.address, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Bearer %s\r\n\r\n", u.Host, u.Host, f.credential.Bearer)
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	secure := tls.Client(&bufferedConn{Conn: conn, reader: reader}, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.roots, ServerName: "other.example.com"})
	err = secure.HandshakeContext(t.Context())
	require.ErrorContains(t, err, "remote error: tls:")
	require.Zero(t, calls.Load())
}
