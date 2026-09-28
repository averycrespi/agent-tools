//go:build integration

package httpproxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
)

// Only closed scalars reach failure logs, never raw records, errors or requests.
// The bounded window may include earlier requests; it does not identify the
// failed CONNECT or establish why admission failed.
type connectFailureEvidence struct {
	Ready, Faulted, Pressure               bool
	QuotaRefusals, HighWater               int64
	HistoryAvailable, WindowFull           bool
	WindowRecords                          int
	LastConnect, LastDecision, LastAllowed bool
	LastInterceptSelected                  bool
}

func (f *proxyFixture) connectFailureSnapshot(ctx context.Context) connectFailureEvidence {
	status := f.traffic.Status(ctx)
	out := connectFailureEvidence{Ready: status.Ready, Faulted: status.Faulted, Pressure: status.Pressure, QuotaRefusals: status.QuotaRefusals}
	const limit = 256
	history, err := f.traffic.HTTPHistory(ctx, 0, limit)
	if err != nil {
		return out
	}
	out.HistoryAvailable = true
	out.HighWater = history.HighWater
	out.WindowRecords = len(history.Records)
	out.WindowFull = len(history.Records) == limit
	if len(history.Records) != 0 {
		last := history.Records[len(history.Records)-1].Admission
		out.LastConnect = last.Target != nil && last.Target.Scheme == "" && last.Target.Method == ""
		if last.Decision != nil {
			out.LastDecision = true
			out.LastAllowed = last.Decision.Allowed
			out.LastInterceptSelected = last.Decision.Transport == contract.HTTPTransportIntercept && last.Decision.Reason == contract.HTTPReasonIntercept
		}
	}
	return out
}

func TestIntegrationConnectFailureSnapshot(t *testing.T) {
	f := fixture(t)
	var upstreamDials atomic.Int64
	f.engine.options.Remote = remote.New(remote.Options{DialContext: func(context.Context, string, string) (net.Conn, error) {
		upstreamDials.Add(1)
		t.Error("CONNECT attempted upstream dial")
		return nil, net.ErrClosed
	}})
	empty := f.connectFailureSnapshot(t.Context())
	require.True(t, empty.Ready)
	require.True(t, empty.HistoryAvailable)
	require.Zero(t, empty.WindowRecords)
	require.False(t, empty.LastDecision)

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("CONNECT dispatched upstream") }))
	defer upstream.Close()
	conn := f.intercept(t, upstream.URL, "http/1.1")
	require.NoError(t, conn.Close())
	selected := f.connectFailureSnapshot(t.Context())
	require.True(t, selected.HistoryAvailable)
	require.Equal(t, 1, selected.WindowRecords)
	require.False(t, selected.WindowFull)
	require.True(t, selected.LastConnect)
	require.True(t, selected.LastDecision)
	require.True(t, selected.LastInterceptSelected)
	require.False(t, selected.LastAllowed)
	require.NotContains(t, fmt.Sprintf("%+v", selected), f.credential.Bearer)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	unavailable := f.connectFailureSnapshot(ctx)
	require.False(t, unavailable.HistoryAvailable)
	require.Zero(t, unavailable.WindowRecords)
	require.False(t, unavailable.LastDecision)
	require.Zero(t, upstreamDials.Load())
}
