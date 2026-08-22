package mcpingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	legacyInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`
	legacyPing       = `{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}`
)

type manualTimer struct {
	callback func()
	stopped  bool
}

func (timer *manualTimer) Stop() bool {
	wasActive := !timer.stopped
	timer.stopped = true
	return wasActive
}

func (timer *manualTimer) Reset(time.Duration) bool {
	wasActive := !timer.stopped
	timer.stopped = false
	return wasActive
}

type manualScheduler struct {
	mu     sync.Mutex
	timers []*manualTimer
}

func (scheduler *manualScheduler) AfterFunc(_ time.Duration, callback func()) Timer {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	timer := &manualTimer{callback: callback}
	scheduler.timers = append(scheduler.timers, timer)
	return timer
}

func (scheduler *manualScheduler) Fire(index int) {
	scheduler.mu.Lock()
	timer := scheduler.timers[index]
	if timer.stopped {
		scheduler.mu.Unlock()
		return
	}
	timer.stopped = true
	callback := timer.callback
	scheduler.mu.Unlock()
	callback()
}

func (scheduler *manualScheduler) Stopped(index int) bool {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return scheduler.timers[index].stopped
}

type subscribingAuthenticator struct {
	mu           sync.RWMutex
	bindings     map[string]Binding
	invalidated  chan struct{}
	subscribedCh chan struct{}
	once         sync.Once
}

func (authenticator *subscribingAuthenticator) Authenticate(_ context.Context, bearer string) (Binding, error) {
	authenticator.mu.RLock()
	defer authenticator.mu.RUnlock()
	binding, ok := authenticator.bindings[bearer]
	if !ok {
		return Binding{}, ErrAuthenticationRequired
	}
	return binding, nil
}

func (authenticator *subscribingAuthenticator) Subscribe(Binding) <-chan struct{} {
	authenticator.once.Do(func() { close(authenticator.subscribedCh) })
	return authenticator.invalidated
}

func newLegacyHarness(t *testing.T, clock *testutil.FakeClock, entropy *testutil.FakeEntropy) (*Handler, *httpboundary.Boundary, *subscribingAuthenticator) {
	t.Helper()
	expires := clock.Now().Add(24 * time.Hour)
	authenticator := &subscribingAuthenticator{
		bindings: map[string]Binding{
			contract.AgentBearerPrefix + "valid": {PrincipalID: "01J00000000000000000000000", CredentialID: "01J00000000000000000000001", ExpiresAt: expires},
			contract.AgentBearerPrefix + "other": {PrincipalID: "01J00000000000000000000002", CredentialID: "01J00000000000000000000003", ExpiresAt: expires},
		},
		invalidated: make(chan struct{}), subscribedCh: make(chan struct{}),
	}
	handler := New(Options{Authenticator: authenticator, Now: clock.Now, Entropy: entropy})
	boundary, err := httpboundary.New(httpboundary.Options{
		Authority: contract.DefaultAuthority,
		Authenticate: func(ctx context.Context, request *http.Request, authority contract.CredentialAuthority) (context.Context, error) {
			return handler.Authenticate(ctx, request, authority)
		},
		Next: handler,
	})
	require.NoError(t, err)
	return handler, boundary, authenticator
}

func legacyRequest(method, body, bearer, sessionID string) *http.Request {
	request := modernRequest(method, body)
	request.Header.Set("Authorization", "Bearer "+contract.AgentBearerPrefix+bearer)
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
		request.Header.Set("Mcp-Protocol-Version", contract.LegacyProtocolVersion)
	}
	return request
}

func initializeLegacy(t *testing.T, boundary *httpboundary.Boundary) string {
	t.Helper()
	request := legacyRequest(http.MethodPost, legacyInitialize, "valid", "")
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	sessionID := response.Header().Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)
	var envelope struct {
		Result struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
	assert.Equal(t, contract.LegacyProtocolVersion, envelope.Result.ProtocolVersion)
	assert.Empty(t, envelope.Result.Capabilities)
	assert.NotContains(t, response.Body.String(), "listChanged")
	return sessionID
}

func TestLegacySessionBindsReauthenticationAndReleasesEveryTerminalPath(t *testing.T) {
	t.Parallel()
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC))
	entropy := testutil.NewFakeEntropy(makeDistinctEntropy(4))
	handler, boundary, authenticator := newLegacyHarness(t, clock, entropy)

	sessionID := initializeLegacy(t, boundary)
	<-authenticator.subscribedCh
	_, _, legacy := handler.Status()
	assert.Equal(t, int64(1), legacy.InUse)

	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, legacyPing, "valid", sessionID))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, legacyPing, "other", sessionID))
	assert.Equal(t, http.StatusNotFound, response.Code)

	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodDelete, "", "valid", sessionID))
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	_, _, legacy = handler.Status()
	assert.Zero(t, legacy.InUse)

	expiredSession := initializeLegacy(t, boundary)
	clock.Advance(contract.LegacyIdleLifetime + time.Second)
	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, legacyPing, "valid", expiredSession))
	assert.Equal(t, http.StatusNotFound, response.Code)
	_, _, legacy = handler.Status()
	assert.Zero(t, legacy.InUse)

	invalidatedSession := initializeLegacy(t, boundary)
	close(authenticator.invalidated)
	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, legacyPing, "valid", invalidatedSession))
	assert.Equal(t, http.StatusNotFound, response.Code)
	_, _, legacy = handler.Status()
	assert.Zero(t, legacy.InUse)

	restarted, restartedBoundary, _ := newLegacyHarness(t, clock, testutil.NewFakeEntropy(makeDistinctEntropy(1)))
	response = httptest.NewRecorder()
	restartedBoundary.ServeHTTP(response, legacyRequest(http.MethodPost, legacyPing, "valid", invalidatedSession))
	assert.Equal(t, http.StatusNotFound, response.Code)
	restarted.Shutdown()
	handler.Shutdown()
}

func TestLegacyIdleTimerClosesSessionWithoutAnotherRequest(t *testing.T) {
	t.Parallel()
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC))
	authenticator := &subscribingAuthenticator{
		bindings: map[string]Binding{
			contract.AgentBearerPrefix + "valid": {
				PrincipalID: "01J00000000000000000000000", CredentialID: "01J00000000000000000000001", ExpiresAt: clock.Now().Add(24 * time.Hour),
			},
		},
		invalidated: make(chan struct{}), subscribedCh: make(chan struct{}),
	}
	scheduler := &manualScheduler{}
	handler := New(Options{
		Authenticator: authenticator,
		Now:           clock.Now,
		Entropy:       testutil.NewFakeEntropy(makeDistinctEntropy(2)),
		AfterFunc:     scheduler.AfterFunc,
	})
	defer handler.Shutdown()
	boundary, err := httpboundary.New(httpboundary.Options{
		Authority: contract.DefaultAuthority,
		Authenticate: func(ctx context.Context, request *http.Request, authority contract.CredentialAuthority) (context.Context, error) {
			return handler.Authenticate(ctx, request, authority)
		},
		Next: handler,
	})
	require.NoError(t, err)
	initializeLegacy(t, boundary)
	scheduler.Fire(0)
	_, _, legacy := handler.Status()
	assert.Zero(t, legacy.InUse)
	assert.True(t, scheduler.Stopped(1), "absolute expiry timer survived idle closure")

	sessionID := initializeLegacy(t, boundary)
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodDelete, "", "valid", sessionID))
	require.Equal(t, http.StatusNoContent, response.Code)
	assert.True(t, scheduler.Stopped(2), "idle timer survived DELETE")
	assert.True(t, scheduler.Stopped(3), "absolute expiry timer survived DELETE")
}

func TestLegacyInitializationReservesNBeforeEntropyAndRejectsNPlusOne(t *testing.T) {
	t.Parallel()
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC))
	entropy := testutil.NewFakeEntropy(makeDistinctEntropy(129))
	handler, boundary, _ := newLegacyHarness(t, clock, entropy)
	defer handler.Shutdown()

	for range 128 {
		initializeLegacy(t, boundary)
	}
	before := entropy.Remaining()
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, legacyInitialize, "valid", ""))
	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	assert.Equal(t, before, entropy.Remaining(), "N+1 initialization consumed entropy")
	_, _, legacy := handler.Status()
	assert.Equal(t, int64(128), legacy.InUse)
	assert.True(t, legacy.Saturated)
}

func makeDistinctEntropy(blocks int) []byte {
	result := make([]byte, blocks*32)
	for block := range blocks {
		for index := range 32 {
			result[block*32+index] = byte(block + index)
		}
	}
	return result
}
