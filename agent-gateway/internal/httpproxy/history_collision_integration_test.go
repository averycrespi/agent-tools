//go:build integration

package httpproxy

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

func TestIntegrationConnectCaptureCollisionDoesNotChangeInnerOutcome(t *testing.T) {
	for _, protocol := range []string{"http/1.1", "h2"} {
		for _, scenario := range []string{"allowed", "invalid"} {
			t.Run(protocol+"/"+scenario, func(t *testing.T) {
				f := fixture(t)
				// The capture clock/entropy deliberately repeat an ID. Authority uses its
				// independent real clock and each inner request still reauthenticates.
				evidence, err := invocation.NewTrafficRepository(f.traffic, testutil.NewFakeClock(time.Now().UTC()), bytes.NewReader(bytes.Repeat([]byte{0xAB}, 128)), func(contract.Invalidation) {})
				require.NoError(t, err)
				admissions, err := invocation.NewAdmissionCoordinator(evidence, f.authority)
				require.NoError(t, err)
				f.engine.options.Evidence, f.engine.options.Admissions = evidence, admissions
				var calls atomic.Int32
				upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					_, _ = io.WriteString(w, "unchanged response")
				}))
				defer upstream.Close()
				f.engine.roots = x509.NewCertPool()
				f.engine.roots.AddCert(upstream.Certificate())
				f.allow(t, upstream.URL, "allow_requests", "", "")
				conn := f.intercept(t, upstream.URL, protocol)
				parent := f.waitHTTPHistory(t, 1).Records[0]
				require.NotNil(t, parent.Admission.Target)
				target := upstream.URL + "/resource"
				if scenario == "invalid" {
					target = upstream.URL + "/%5cprivate-canary"
				}
				request, err := http.NewRequestWithContext(t.Context(), "GET", target, nil)
				require.NoError(t, err)
				var response *http.Response
				if protocol == "h2" {
					client, clientErr := (&http2.Transport{}).NewClientConn(conn)
					require.NoError(t, clientErr)
					defer func() { require.NoError(t, client.Close()) }()
					response, err = client.RoundTrip(request)
				} else {
					require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
					require.NoError(t, request.Write(conn))
					response, err = http.ReadResponse(bufio.NewReader(conn), request)
				}
				require.NoError(t, err)
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				if scenario == "allowed" {
					require.Equal(t, 200, response.StatusCode)
					require.Equal(t, "unchanged response", string(body))
					require.EqualValues(t, 1, calls.Load())
				} else {
					require.Equal(t, 400, response.StatusCode)
					require.Zero(t, calls.Load())
				}
				require.Eventually(t, func() bool { return f.traffic.Status(t.Context()).QuotaRefusals > 0 }, time.Second, time.Millisecond)
				history, err := f.traffic.HTTPHistory(t.Context(), 0, 10)
				require.NoError(t, err)
				require.Len(t, history.Records, 1, "invalid inner capture is discarded, not stored over its CONNECT")
				require.Equal(t, parent.Admission, history.Records[0].Admission)
				require.True(t, f.traffic.Healthy())
			})
		}
	}
}
