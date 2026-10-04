//go:build integration

package httpproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
	"github.com/stretchr/testify/require"
)

type failedDiagnosticResolver struct{}

func (failedDiagnosticResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return nil, errors.New("private-resolver-error-canary")
}

func TestIntegrationProxyRejectionDiagnostics(t *testing.T) {
	for _, mode := range []string{"request-resolution", "connect-resolution", "inner-resolution", "traffic-unavailable", "policy-denial"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			var output bytes.Buffer
			observer := diagnostics.New(&output, diagnostics.Warn)
			t.Cleanup(func() { observer.Finish(nil); <-observer.Done() })
			f.engine.options.Diagnostics = observer
			method, target := http.MethodPost, "http://127.0.0.1:1/private-path-canary?secret=query-canary"
			if mode == "request-resolution" || mode == "connect-resolution" || mode == "inner-resolution" {
				f.engine.options.Remote = remote.New(remote.Options{Resolver: failedDiagnosticResolver{}})
				target = "http://private-host-canary.example/private-path-canary?secret=query-canary"
			}
			if mode == "connect-resolution" {
				method, target = http.MethodConnect, "http://private-host-canary.example:443"
			}
			if mode == "traffic-unavailable" {
				f.traffic.BeginDrain()
			}
			r := httptest.NewRequest(method, target, bytes.NewBufferString("private-body-canary"))
			if method == http.MethodConnect {
				r.RequestURI, r.Host, r.ContentLength = "private-host-canary.example:443", "private-host-canary.example:443", 0
				r.Body = http.NoBody
			}
			r.Header.Set("Proxy-Authorization", "Bearer "+f.credential.Bearer)
			var inside *intercepted
			if mode == "inner-resolution" {
				lease, err := f.authority.Authenticate(t.Context(), f.credential.Bearer)
				require.NoError(t, err)
				destination, err := httppolicy.NewDestination("private-host-canary.example", 443)
				require.NoError(t, err)
				inside = &intercepted{destination: destination, bearer: f.credential.Bearer, binding: lease.Binding(), sni: destination.Host()}
				lease.Release()
				r.RequestURI, r.URL.Scheme, r.URL.Host, r.Host = "/private-path-canary", "", "", destination.Host()
				r.Header.Del("Proxy-Authorization")
			}
			r.Header.Set("Authorization", "Bearer private-upstream-token-canary")
			response := httptest.NewRecorder()
			f.engine.handle(response, r, inside)
			status := http.StatusBadGateway
			if mode == "traffic-unavailable" {
				status = http.StatusServiceUnavailable
			}
			if mode == "policy-denial" {
				status = http.StatusForbidden
			}
			require.Equal(t, status, response.Code)
			require.Equal(t, http.StatusText(status)+"\n", response.Body.String())
			require.True(t, observer.Finish(nil))
			history, err := f.traffic.HTTPHistory(t.Context(), 0, 10)
			require.NoError(t, err)
			if mode == "policy-denial" {
				require.Empty(t, output.String(), "ordinary policy denials remain in traffic, not infrastructure warnings")
				require.Len(t, history.Records, 1)
				return
			}
			require.Empty(t, history.Records, "the diagnostic must cover rejection before a traffic row exists")
			var record map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &record))
			require.Equal(t, "http_proxy_rejected", record["event"])
			require.Equal(t, "WARN", record["level"])
			require.Equal(t, response.Header().Get("Gateway-Request-ID"), record["proxy_id"])
			require.Len(t, record["proxy_id"], 32)
			require.Equal(t, "unavailable", record["cause"])
			stage := "resolution"
			if mode == "traffic-unavailable" {
				stage = "traffic_admission"
			}
			require.Equal(t, stage, record["stage"])
			for _, secret := range []string{"canary", f.credential.Bearer, f.credential.Principal.ID} {
				require.NotContains(t, output.String(), secret)
			}
		})
	}
}
