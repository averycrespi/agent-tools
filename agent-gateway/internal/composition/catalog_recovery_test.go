package composition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/runtimes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The scheduler fires real owner callbacks without waiting for the five-minute grid.
// It never invents a retry: a missing callback is the regression signal.
type recoveryScheduler struct{ calls chan *recoveryTimer }
type recoveryTimer struct {
	delay    time.Duration
	callback func()
	stopped  atomic.Bool
}

func (timer *recoveryTimer) Stop() bool { return !timer.stopped.Swap(true) }
func (scheduler *recoveryScheduler) AfterFunc(delay time.Duration, callback func()) runtimes.Timer {
	timer := &recoveryTimer{delay: delay, callback: callback}
	scheduler.calls <- timer
	return timer
}
func (scheduler *recoveryScheduler) next(t *testing.T) *recoveryTimer {
	t.Helper()
	select {
	case timer := <-scheduler.calls:
		return timer
	case <-time.After(3 * time.Second):
		t.Fatal("no scheduled recovery: catalog runtime loss stranded the server")
		return nil
	}
}
func (timer *recoveryTimer) fire(t *testing.T) {
	t.Helper()
	require.False(t, timer.stopped.Load())
	timer.callback()
}

type recoveryTransport struct {
	downstream.Transport
	close func(context.Context) error
}

func (transport *recoveryTransport) Close(ctx context.Context) error { return transport.close(ctx) }

func TestScheduledCatalogSessionLossRecoversUsableTools(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(fmt.Sprintf("stop_confirmed=%t", confirmed), func(t *testing.T) { testScheduledCatalogSessionLoss(t, confirmed) })
	}
}

func testScheduledCatalogSessionLoss(t *testing.T, confirmed bool) {
	t.Helper()
	var loseSession atomic.Bool
	var initializations, lists, calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode fixture request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", contract.MediaTypeJSON)
		switch request.Method {
		case "initialize":
			session := initializations.Add(1)
			w.Header().Set("Mcp-Session-Id", fmt.Sprintf("session-%d", session))
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"fixture","version":"1"}}}`, request.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			lists.Add(1)
			if loseSession.Load() {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"one","inputSchema":{"type":"object"}}]}}`, request.ID)
		case "tools/call":
			if calls.Add(1) == 1 {
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32603,"message":"fixture failure"}}`, request.ID)
				return
			}
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[]}}`, request.ID)
		default:
			t.Errorf("unexpected method %q", request.Method)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer upstream.Close()
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	scheduler := &recoveryScheduler{calls: make(chan *recoveryTimer, 16)}
	stopStarted, releaseStop := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseStop) })
	defer release()
	var transports atomic.Int32
	built, err := newWithHooks(options, constructorHooks{scheduler: scheduler, newCoordinator: func(transport downstream.Transport) (*downstream.Coordinator, error) {
		first := transports.Add(1) == 1
		return downstream.NewCoordinator(&recoveryTransport{Transport: transport, close: func(ctx context.Context) error {
			if first {
				close(stopStarted)
				select {
				case <-releaseStop:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			err := transport.Close(ctx)
			if first && !confirmed {
				return errors.Join(err, errors.New("fixture stop uncertainty"))
			}
			return err
		}})
	}})
	require.NoError(t, err)
	defer built.shutdownConstructed()
	defer release()
	desired := enableCompositionServer(t, built.servers, createServerWithTransport(t, built.servers, "recovery", contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: upstream.URL + "/mcp", ProtocolMode: contract.ProtocolLegacy, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}}))
	require.NoError(t, built.Start(t.Context()))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.True(t, built.manager.Wait(ctx))
	oldRuntime := built.RuntimeStatus(desired.ID).RuntimeID
	require.NotNil(t, oldRuntime, "startup status: %+v; initializations=%d lists=%d", built.RuntimeStatus(desired.ID), initializations.Load(), lists.Load())
	old, ok := built.activeCatalog.Routes().ResolveCall("recovery.one")
	require.True(t, ok)
	require.Len(t, built.activeCatalog.CurrentSnapshot().Descriptors, 1)
	failedLease, err := old.Capability.Acquire(t.Context())
	require.NoError(t, err)
	require.NotNil(t, failedLease.Execute(t.Context(), json.RawMessage(`{}`)).Response.Error)
	assert.Equal(t, int32(1), calls.Load())
	poll := scheduler.next(t)
	require.Greater(t, poll.delay, time.Duration(0))
	loseSession.Store(true)
	pollDone := make(chan struct{})
	go func() { poll.callback(); close(pollDone) }()
	select {
	case <-stopStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("catalog runtime loss never reached verified stop")
	}
	assert.Equal(t, int32(1), initializations.Load())
	select {
	case <-scheduler.calls:
		t.Fatal("retry scheduled before stop confirmation")
	default:
	}
	release()
	select {
	case <-pollDone:
	case <-time.After(3 * time.Second):
		t.Fatal("poll did not finish after transport close")
	}
	assert.Equal(t, contract.ActiveCatalogUnavailable, built.activeCatalog.Status(desired.ID).State)
	_, ok = built.activeCatalog.Routes().ResolveCall("recovery.one")
	require.False(t, ok, "lost runtime retained a callable route")
	assert.Empty(t, built.activeCatalog.CurrentSnapshot().Descriptors)
	assert.Equal(t, int32(2), lists.Load())
	if !confirmed {
		require.Eventually(t, func() bool {
			status := built.RuntimeStatus(desired.ID)
			return status.Reason != nil && *status.Reason == contract.ReasonStopUnconfirmed
		}, time.Second, time.Millisecond)
		select {
		case <-scheduler.calls:
			t.Fatal("unconfirmed stop scheduled replacement")
		default:
		}
		assert.Equal(t, int32(1), initializations.Load())
		assert.Equal(t, int32(1), calls.Load())
		return
	}
	retry := scheduler.next(t)
	assert.Equal(t, time.Second, retry.delay)
	require.Equal(t, contract.RuntimeRetryWait, built.RuntimeStatus(desired.ID).State)
	assert.Nil(t, built.RuntimeStatus(desired.ID).RuntimeID)
	assert.Equal(t, int32(1), initializations.Load(), "replacement started before retry admission")
	loseSession.Store(false)
	retry.fire(t)
	require.True(t, built.manager.Wait(ctx))
	current := built.RuntimeStatus(desired.ID)
	require.NotNil(t, current.RuntimeID)
	assert.NotEqual(t, *oldRuntime, *current.RuntimeID)
	assert.Equal(t, contract.RuntimeActive, current.State)
	assert.Equal(t, contract.ActiveCatalogCurrent, built.activeCatalog.Status(desired.ID).State)
	require.Len(t, built.activeCatalog.CurrentSnapshot().Descriptors, 1)
	fresh, ok := built.activeCatalog.Routes().ResolveCall("recovery.one")
	require.True(t, ok)
	_, err = old.Capability.Acquire(t.Context())
	require.Error(t, err, "old capability regained authority")
	lease, err := fresh.Capability.Acquire(t.Context())
	require.NoError(t, err)
	assert.Empty(t, lease.Execute(t.Context(), json.RawMessage(`{}`)).Failure)
	assert.Equal(t, int32(2), initializations.Load())
	assert.Equal(t, int32(3), lists.Load())
	assert.Equal(t, int32(2), calls.Load(), "recovery must not replay the failed invocation")
	nextPoll := scheduler.next(t)
	assert.Greater(t, nextPoll.delay, time.Second)
	select {
	case duplicate := <-scheduler.calls:
		t.Fatalf("duplicate lifecycle timer: %s", duplicate.delay)
	default:
	}
}
