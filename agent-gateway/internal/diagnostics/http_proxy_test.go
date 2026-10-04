package diagnostics

import (
	"bytes"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestHTTPProxyDiagnosticClosedBoundary(t *testing.T) {
	for _, level := range []Level{Warn, Info, Debug} {
		var output bytes.Buffer
		adapter := New(&output, level)
		adapter.HTTPProxy(Facts{Event: HTTPProxyRejected, Stage: ProxyTraffic, Cause: Expired, Duration: 300 * time.Millisecond})
		require.True(t, adapter.Finish(nil))
		got := records(t, output.Bytes())
		require.Len(t, got, 1)
		require.Equal(t, "http_proxy_rejected", got[0]["event"])
		require.Equal(t, "traffic_admission", got[0]["stage"])
		require.Equal(t, "expired", got[0]["cause"])
		require.Equal(t, "WARN", got[0]["level"])
		require.Equal(t, "inspect_status", got[0]["action"])
		require.EqualValues(t, 300, got[0]["duration_ms"])
		require.Len(t, got[0], 9)
	}
	for _, mutate := range []func(*Facts){
		func(f *Facts) { f.InvocationID = "secret-canary" },
		func(f *Facts) { f.ProxyID = "secret-canary" },
		func(f *Facts) { f.Upstream = 1 },
		func(f *Facts) { f.Call = 1 },
		func(f *Facts) { f.Mutation = 1 },
		func(f *Facts) { f.Stage = IntentCleanup },
		func(f *Facts) { f.Stage = ProxyConfirmation + 1 },
		func(f *Facts) { f.Cause = Rejected },
		func(f *Facts) { f.Duration = contract.DiagnosticElapsedMaximum + time.Nanosecond },
	} {
		f := validEventExample(HTTPProxyRejected)
		mutate(&f)
		require.False(t, validFacts(f))
	}
	for stage := ProxyCapacity; stage <= ProxyConnect; stage++ {
		f := Facts{Event: HTTPProxyFailure, Stage: stage, Cause: Unavailable, ProxyID: "0123456789abcdef0123456789abcdef"}
		require.True(t, validFacts(f))
		f.ProxyID = "0123456789ABCDEF0123456789ABCDEF"
		require.False(t, validFacts(f))
	}
	// Proxy stages must not expand the durability event vocabulary.
	f := validEventExample(DurabilityFailure)
	f.Stage = ProxyTraffic
	require.False(t, validFacts(f))
}
