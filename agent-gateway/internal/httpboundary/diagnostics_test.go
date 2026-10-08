package httpboundary

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

func TestMainHTTPPanicRetainsLocationWithoutPayload(t *testing.T) {
	for _, abort := range []bool{false, true} {
		var output bytes.Buffer
		adapter := diagnostics.New(&output, diagnostics.Warn)
		var calls, panicLine atomic.Int64
		boundary, err := New(Options{Diagnostics: adapter, Authority: contract.DefaultAuthority, Ready: func() bool { return true }, Authenticate: func(ctx context.Context, _ *http.Request, _ contract.CredentialAuthority) (context.Context, error) {
			return ctx, nil
		}, Next: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			body, _ := io.ReadAll(r.Body)
			if abort {
				panic(http.ErrAbortHandler)
			}
			_, _, line, _ := runtime.Caller(0)
			panicLine.Store(int64(line + 2))
			panic(string(body) + r.Header.Get("Authorization"))
		})})
		require.NoError(t, err)
		server := httptest.NewUnstartedServer(boundary)
		server.Config.ErrorLog = diagnostics.HTTPErrorLog(adapter)
		server.Start()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/v2/principals", strings.NewReader(`{"private":"opaque-sensitive-payload"}`))
		require.NoError(t, err)
		request.Host = contract.DefaultAuthority
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer actual-main-secret")
		response, err := server.Client().Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		server.Close()
		require.Error(t, err, "preserve connection abort instead of inventing a public response")
		require.EqualValues(t, 1, calls.Load())
		require.True(t, adapter.Finish(nil))
		require.NotContains(t, output.String(), "opaque-sensitive-payload")
		require.NotContains(t, output.String(), "actual-main-secret")
		if abort {
			require.Empty(t, output.String())
		} else {
			require.Contains(t, output.String(), "panic detail withheld")
			require.Contains(t, output.String(), "/api/v2/principals")
			require.Contains(t, output.String(), fmt.Sprintf("diagnostics_test.go:%d", panicLine.Load()))
			require.Contains(t, output.String(), "TestMainHTTPPanicRetainsLocationWithoutPayload")
		}
	}
}
