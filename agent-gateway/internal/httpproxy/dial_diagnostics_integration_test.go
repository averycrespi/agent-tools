//go:build integration

package httpproxy

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
	"github.com/stretchr/testify/require"
)

func TestIntegrationDialTimeoutRetainsDrivingAndLastCause(t *testing.T) {
	f := fixture(t)
	const origin = "http://approved.example:8999"
	allowNamedAddress(t, f, origin, "allow_requests", "")
	resolver := &addressFixtureResolver{answers: []netip.Addr{netip.MustParseAddr("::1"), netip.MustParseAddr("127.0.0.1")}}
	var dials atomic.Int64
	f.engine.options.Remote = remote.New(remote.Options{Resolver: resolver, DialContext: func(context.Context, string, string) (net.Conn, error) {
		if dials.Add(1) == 1 {
			return nil, context.DeadlineExceeded
		}
		return nil, fmt.Errorf("second candidate: %w", syscall.ECONNREFUSED)
	}})
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Warn)
	t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
	f.engine.options.Diagnostics = adapter
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+"/", nil)
	require.NoError(t, err)
	request.Header.Set("Proxy-Authorization", "Bearer "+f.credential.Bearer)
	response, err := f.client(t).Do(request)
	require.NoError(t, err)
	assertFailureResponse(t, response, 504)
	require.Eventually(t, func() bool { f.engine.mu.Lock(); defer f.engine.mu.Unlock(); return f.engine.work == 0 }, time.Second, time.Millisecond)
	require.True(t, adapter.Finish(nil))
	require.EqualValues(t, 2, dials.Load())
	require.EqualValues(t, 1, resolver.calls.Load())
	for _, fact := range []string{"attempted=2 available=2", "last_family=ipv4", "timeout_candidate=1", "context deadline exceeded", "last_native=second candidate: connection refused", "application_dispatch=not_started"} {
		require.Contains(t, output.String(), fact)
	}
	require.NotContains(t, output.String(), f.credential.Bearer)
}
