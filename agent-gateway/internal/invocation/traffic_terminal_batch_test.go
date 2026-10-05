package invocation

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestTrafficTerminalSnapshotsCopyDiagnosticsBeforeQueueing(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var armed atomic.Bool
	var once sync.Once
	s, _ := trafficFixture(t, nil, func(point string) error {
		if point == "before_begin" && armed.Load() {
			once.Do(func() { close(entered); <-release })
		}
		return nil
	})
	observation := recordMCP(t, s, trafficPrepared(1))
	armed.Store(true)
	completion := trafficCompletion()
	completion.Class = contract.TerminalDownstreamFailure
	diagnostic := &contract.FailureDiagnostics{GatewayObserved: contract.FailureObservation{Source: "protocol", Reason: "rpc_error"}}
	done := make(chan error, 1)
	go func() { done <- s.ObserveMCPCompletion(observation, completion, diagnostic) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("terminal observation waited for SQL")
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not enter")
	}
	diagnostic.GatewayObserved.Reason = "SECRET-unvalidated"
	completion.Class = contract.TerminalSucceeded
	unblock()
	waitTraffic(t, s)
	h, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, h.Records, 1)
	require.Equal(t, contract.TerminalDownstreamFailure, *h.Records[0].TerminalClass)
	require.NotNil(t, h.Records[0].Diagnostics)
	require.Equal(t, "rpc_error", h.Records[0].Diagnostics.GatewayObserved.Reason)
}

func TestTrafficInvalidCaptureDropsWithoutFaultingWriter(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	invalid := trafficPrepared(1)
	invalid.admission.MCP.RedactedArguments = []byte(`not-json-private-canary`)
	require.Nil(t, s.ObserveMCP(invalid))
	valid := recordMCP(t, s, trafficPrepared(2))
	bad := trafficCompletion()
	bad.Class = "unvalidated-private-canary"
	require.ErrorIs(t, s.ObserveMCPCompletion(valid, bad, nil), ErrInvalidInput)
	require.True(t, s.Healthy())
	h, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, h.Records, 1)
	require.Nil(t, h.Records[0].TerminalClass)
}

func TestTrafficObservationHasNoExecutionOwnership(t *testing.T) {
	var visit func(reflect.Type)
	seen := map[reflect.Type]bool{}
	visit = func(typ reflect.Type) {
		if seen[typ] {
			return
		}
		seen[typ] = true
		require.NotEqual(t, reflect.Func, typ.Kind(), "callbacks cannot enter the recorder")
		require.NotEqual(t, reflect.Chan, typ.Kind(), "settlement channels cannot enter the recorder")
		require.NotContains(t, typ.String(), "context.Context")
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice:
			visit(typ.Elem())
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				visit(typ.Field(i).Type)
			}
		}
	}
	visit(reflect.TypeFor[TrafficObservation]())
	// trafficRequest has only a bounded encoded diagnostic interface; no live
	// context, material, result channel, lease or cleanup closure is retained.
	typ := reflect.TypeFor[trafficRequest]()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		require.NotContains(t, field.Type.String(), "Context")
		require.NotEqual(t, reflect.Func, field.Type.Kind())
		require.NotEqual(t, reflect.Chan, field.Type.Kind())
	}
}
