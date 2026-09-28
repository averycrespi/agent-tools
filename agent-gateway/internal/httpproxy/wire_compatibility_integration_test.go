//go:build integration

package httpproxy

import (
	"bufio"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
)

func TestIntegrationURLWireCompatibilityAllIngress(t *testing.T) {
	for _, protocol := range []string{"http", "http/1.1", "h2"} {
		t.Run(protocol, func(t *testing.T) {
			f := fixture(t)
			type capture struct{ host, target, authorization, proxy string }
			received := make(chan capture, 1)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- capture{r.Host, r.RequestURI, r.Header.Get("Authorization"), r.Header.Get("Proxy-Authorization")}
				w.WriteHeader(http.StatusNoContent)
			})
			var upstream *httptest.Server
			if protocol == "http" {
				upstream = httptest.NewServer(handler)
			} else {
				upstream = httptest.NewTLSServer(handler)
			}
			defer upstream.Close()
			u, err := url.Parse(upstream.URL)
			require.NoError(t, err)
			materialID, wantAuthorization := "", ""
			if protocol != "http" {
				f.engine.roots = x509.NewCertPool()
				f.engine.roots.AddCert(upstream.Certificate())
				addr, err := netip.ParseAddrPort(u.Host)
				require.NoError(t, err)
				material, err := f.materials.Create(audit.WithSystem(t.Context()), httpcredentials.Definition{Name: "compatibility", Boundary: httpcredentials.Boundary{Host: addr.Addr().String(), Port: addr.Port()}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte("wire-fixture-canary"))
				require.NoError(t, err)
				materialID = material.ID
				wantAuthorization = "Bearer wire-fixture-canary"
			}
			// Unchanged any-path grants intentionally reach new paths and, on HTTPS,
			// inject their selected credential. The destination is never path-derived.
			f.allow(t, upstream.URL, "allow_requests", "", materialID)
			client := f.client(t)
			for _, target := range []string{
				"/https://example.com", "//example.com/x", "/@anthropic-ai/sdk/-/sdk-0.124.0.tgz", "/word-wrap/-/word-wrap-1.2.5.tgz",
				"/@scope%2fpkg", "/@scope%2Fpkg", "/%40scope/pkg", "/@scope/pkg",
				"/!$&'()*+,;=:@", "/%21%24%26%27%28%29%2a%2B%2c%3b%3D%3a%40",
				"/a/b", "/a%2Fb", "/a%252Fb", "/a//b", "/api", "/%61pi",
				"/caf%C3%a9", "/cafe%CC%81", "/%252e%252e/%255c", "/?a=1&a=2+3&v=%00%5c%ff", "/?", "/",
			} {
				t.Run(target, func(t *testing.T) {
					var response *http.Response
					if protocol == "http" {
						response = f.request(t, client, "GET", upstream.URL+target, nil)
					} else {
						conn := f.intercept(t, upstream.URL, protocol)
						require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
						defer func() { _ = conn.Close() }()
						if protocol == "h2" {
							cc, e := (&http2.Transport{}).NewClientConn(conn)
							require.NoError(t, e)
							defer func() { _ = cc.Close() }()
							req, e := http.NewRequestWithContext(t.Context(), "GET", upstream.URL+target, nil)
							require.NoError(t, e)
							response, err = cc.RoundTrip(req)
						} else {
							_, err = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target, u.Host)
							require.NoError(t, err)
							response, err = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "GET"})
						}
						require.NoError(t, err)
					}
					_, err = io.Copy(io.Discard, response.Body)
					require.NoError(t, err)
					require.NoError(t, response.Body.Close())
					require.Equal(t, http.StatusNoContent, response.StatusCode)
					select {
					case got := <-received:
						require.Equal(t, capture{u.Host, target, wantAuthorization, ""}, got)
					case <-time.After(time.Second):
						t.Fatal("missing upstream capture")
					}
				})
			}
			records, err := f.traffic.HTTPHistory(t.Context(), 0, 100)
			require.NoError(t, err)
			for _, record := range records.Records {
				if record.Admission.Target != nil && record.Admission.Target.Method == "GET" {
					require.True(t, record.Admission.Decision.Allowed)
					if materialID != "" {
						require.Equal(t, materialID, record.Admission.Decision.Credential.ID)
					} else {
						require.Nil(t, record.Admission.Decision.Credential)
					}
				}
			}
		})
	}
}
