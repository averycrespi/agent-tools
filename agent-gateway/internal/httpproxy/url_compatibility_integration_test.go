//go:build integration

package httpproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
)

type registryResolver struct{}

func (registryResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if host != "registry.npmjs.org" {
		return nil, fmt.Errorf("unexpected fixture host")
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func TestIntegrationScopedRegistryURLForwarding(t *testing.T) {
	f := fixture(t)
	received := make(chan string, 8)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Host + " " + r.RequestURI
		_, _ = io.WriteString(w, "registry fixture")
	}))
	cert, err := f.engine.options.Signer.Certificate("registry.npmjs.org")
	require.NoError(t, err)
	upstream.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*cert}}
	upstream.StartTLS()
	defer upstream.Close()
	f.engine.roots = f.roots
	f.engine.options.Remote = remote.New(remote.Options{Resolver: registryResolver{}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "127.0.0.1:443" {
			return nil, fmt.Errorf("unexpected fixture destination")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}})
	// Existing host-wide request authorization, not a tunnel or default allow.
	_, err = f.authority.PutHTTPGrant(audit.WithSystem(t.Context()), "", "", authorization.HTTPGrantInput{PrincipalID: f.credential.Principal.ID, Policy: []byte(`{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"registry.npmjs.org","port":443},"methods":{"values":["GET"]},"path":{"kind":"any"}},"allow_private":true}`)})
	require.NoError(t, err)
	for _, protocol := range []string{"http/1.1", "h2"} {
		for _, path := range []string{"/@anthropic-ai/sdk/-/sdk-0.124.0.tgz", "/word-wrap/-/word-wrap-1.2.5.tgz"} {
			t.Run(protocol+path, func(t *testing.T) {
				conn := f.intercept(t, "https://registry.npmjs.org:443", protocol)
				require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
				var response *http.Response
				if protocol == "h2" {
					cc, e := (&http2.Transport{}).NewClientConn(conn)
					require.NoError(t, e)
					defer func() { _ = cc.Close() }()
					req, e := http.NewRequestWithContext(t.Context(), "GET", "https://registry.npmjs.org"+path, nil)
					require.NoError(t, e)
					response, err = cc.RoundTrip(req)
				} else {
					_, err = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: registry.npmjs.org\r\nConnection: close\r\n\r\n", path)
					require.NoError(t, err)
					response, err = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "GET"})
				}
				require.NoError(t, err)
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.Equal(t, "registry fixture", string(body))
				if response.StatusCode == http.StatusOK {
					select {
					case got := <-received:
						require.Equal(t, "registry.npmjs.org:443 "+path, got)
					case <-time.After(time.Second):
						t.Fatal("upstream did not receive authorized target")
					}
				}
			})
		}
	}
}
