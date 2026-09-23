//go:build integration

package httpproxy

import (
	"bufio"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/stretchr/testify/require"
)

func TestIntegrationProxyAdmissionFailureAndPrincipalIsolation(t *testing.T) {
	f := fixture(t)
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = io.WriteString(w, "ok") }))
	defer upstream.Close()
	f.allow(t, upstream.URL, "allow_requests", "", "")
	client := f.client(t)
	client.Transport.(*http.Transport).DisableKeepAlives = false
	response := f.request(t, client, "GET", upstream.URL+"/", nil)
	_, err := io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, 200, response.StatusCode)
	ctx := audit.WithSystem(t.Context())
	principal, err := f.authority.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "No private permission", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	credential, err := f.authority.IssueCredential(ctx, principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	allow := contract.HTTPDefaultAllow
	_, err = f.authority.PatchPrincipal(ctx, principal.Principal.ID, authorization.PatchPrincipalRequest{ExpectedRevision: credential.Principal.Revision, HTTPDefault: &allow})
	require.NoError(t, err)
	var reused atomic.Bool
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }}
	request, err := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(), trace), "GET", upstream.URL+"/", nil)
	require.NoError(t, err)
	request.Header.Set("Proxy-Authorization", "Bearer "+credential.Bearer)
	response, err = client.Do(request)
	require.NoError(t, err)
	require.Equal(t, 403, response.StatusCode)
	_, _ = io.Copy(io.Discard, response.Body)
	require.NoError(t, response.Body.Close())
	require.True(t, reused.Load())
	require.EqualValues(t, 1, calls.Load())
	require.NoError(t, f.traffic.Close())
	response = f.request(t, client, "GET", upstream.URL+"/", nil)
	require.GreaterOrEqual(t, response.StatusCode, 400)
	require.EqualValues(t, 1, calls.Load())
}

func TestIntegrationCredentialConflictOrMissingMaterialNeverDials(t *testing.T) {
	for _, mode := range []string{"conflict", "key-loss"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			var connections atomic.Int64
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
			upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			upstream.StartTLS()
			defer upstream.Close()
			f.engine.roots = x509.NewCertPool()
			f.engine.roots.AddCert(upstream.Certificate())
			u, err := url.Parse(upstream.URL)
			require.NoError(t, err)
			address := netip.MustParseAddrPort(u.Host)
			makeMaterial := func(name string) httpcredentials.Resource {
				material, err := f.materials.Create(audit.WithSystem(t.Context()), httpcredentials.Definition{Name: name, Boundary: httpcredentials.Boundary{Host: address.Addr().String(), Port: address.Port()}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte("fixture-secret"))
				require.NoError(t, err)
				return material
			}
			material := makeMaterial("first")
			f.allow(t, upstream.URL, "allow_requests", "", material.ID)
			if mode == "conflict" {
				second := makeMaterial("second")
				f.allow(t, upstream.URL, "allow_requests", "", second.ID)
			} else {
				f.backend.mu.Lock()
				clear(f.backend.items)
				f.backend.mu.Unlock()
			}
			conn := f.intercept(t, upstream.URL, "http/1.1")
			_, err = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\n\r\n", u.Host)
			require.NoError(t, err)
			response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "GET"})
			require.NoError(t, err)
			require.Equal(t, 403, response.StatusCode)
			require.NoError(t, response.Body.Close())
			require.Zero(t, connections.Load())
		})
	}
}

func TestProxyOccupancyRejectsExcessWithoutTransferringSlots(t *testing.T) {
	e := &Engine{principals: make(map[string]int)}
	for range contract.HTTPProxyWork {
		require.True(t, e.acquire(""))
	}
	require.False(t, e.acquire(""))
	for range contract.HTTPProxyWork {
		e.release("")
	}
	for range contract.HTTPProxyPrincipalWork {
		require.True(t, e.acquire("principal-a"))
	}
	require.False(t, e.acquire("principal-a"))
	require.True(t, e.acquire("principal-b"))
	e.release("principal-b")
	for range contract.HTTPProxyPrincipalWork {
		e.release("principal-a")
	}
	require.Empty(t, e.principals)
	require.Zero(t, e.work)
	e.draining = true
	require.False(t, e.acquire(""))
	require.False(t, e.acquire("principal-a"))
}
