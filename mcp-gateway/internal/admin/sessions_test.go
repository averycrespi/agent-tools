package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionAuditExcludesSecretsAndUnauthenticatedFailures(t *testing.T) {
	ctx := t.Context()
	store, _ := newStore(t)
	clock := testutil.NewFakeClock(testNow)
	service := NewService(store, clock, newDeterministicEntropy())
	sink := new(memorySink)
	parent, err := service.Initialize(ctx, sink)
	require.NoError(t, err)
	manager := NewSessionManager(service, clock, newSessionEntropy(4))
	t.Cleanup(manager.Shutdown)
	reader, err := audit.NewRepository(store)
	require.NoError(t, err)
	_, err = manager.Exchange(ctx, "invalid-bearer")
	require.ErrorIs(t, err, ErrAuthenticationRequired)
	page, err := reader.List(ctx, audit.Query{Limit: 100, Filters: contract.AuditFilters{Category: "admin_session"}})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	session, err := manager.Exchange(ctx, sink.value)
	require.NoError(t, err)
	_, err = manager.Authenticate(ctx, "", session.ID, "bad-csrf", true)
	require.ErrorIs(t, err, ErrCSRF)
	require.NoError(t, manager.LogoutAuthenticated(ctx, session.ID))
	page, err = reader.List(ctx, audit.Query{Limit: 100, Filters: contract.AuditFilters{Category: "admin_session"}})
	require.NoError(t, err)
	require.Len(t, page.Items, 4)
	assert.Equal(t, "logout", page.Items[0].Action)
	assert.Equal(t, "sign_in", page.Items[2].Action)
	for _, summary := range page.Items {
		assert.Equal(t, contract.AuditOperator, summary.Actor.Type)
		assert.Equal(t, parent.ID, summary.Actor.Credential.ID)
		assert.Equal(t, parent.Fingerprint, summary.Actor.Credential.Fingerprint)
		item, err := reader.Read(ctx, summary.ID, page.History.Generation)
		require.NoError(t, err)
		contents, err := json.Marshal(item)
		require.NoError(t, err)
		for _, secret := range []string{sink.value, session.ID, session.CSRFToken} {
			assert.NotContains(t, string(contents), secret)
		}
	}
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER reject_session_outcome BEFORE INSERT ON control_audit_events WHEN json_extract(NEW.event, '$.phase') = 'outcome' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
		return err
	}))
	_, err = manager.Exchange(ctx, sink.value)
	require.Error(t, err)
	assert.Zero(t, manager.Status().InUse, "undocumented session authority must not be returned or retained")
}

func TestSessionCookieCSRFIdleAndAbsoluteExpiry(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	clock := testutil.NewFakeClock(testNow)
	service := NewService(store, clock, newDeterministicEntropy())
	sink := new(memorySink)
	_, err := service.Initialize(ctx, sink)
	require.NoError(t, err)
	manager := NewSessionManager(service, clock, newSessionEntropy(4))
	t.Cleanup(manager.Shutdown)

	created, err := manager.Exchange(ctx, sink.value)
	require.NoError(t, err)
	cookie := created.Cookie()
	assert.Equal(t, contract.SessionCookieName, cookie.Name)
	assert.Equal(t, created.ID, cookie.Value)
	assert.Equal(t, "/", cookie.Path)
	assert.True(t, cookie.HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	assert.Empty(t, cookie.Domain)
	assert.False(t, cookie.Secure)
	assert.NotEqual(t, created.ID, created.CSRFToken)
	subscription, err := manager.Subscribe(created.ID)
	require.NoError(t, err)

	_, err = manager.Authenticate(ctx, "", created.ID, "wrong", true)
	assert.ErrorIs(t, err, ErrCSRF)
	_, err = manager.Authenticate(ctx, "", created.ID, created.CSRFToken, true)
	require.NoError(t, err)
	clock.Advance(contract.AdminSessionIdleLifetime - time.Second)
	_, err = manager.Authenticate(ctx, "", created.ID, created.CSRFToken, true)
	require.NoError(t, err)
	clock.Advance(contract.AdminSessionIdleLifetime + time.Second)
	manager.Sweep(ctx)
	assertChannelClosed(t, created.Done)
	assertChannelClosed(t, subscription)
	assert.Equal(t, int64(0), manager.Status().InUse)

	absolute, err := manager.Exchange(ctx, sink.value)
	require.NoError(t, err)
	for clock.Now().Before(absolute.AbsoluteExpiresAt) {
		remaining := absolute.AbsoluteExpiresAt.Sub(clock.Now())
		advance := contract.AdminSessionIdleLifetime - time.Second
		if remaining < advance {
			advance = remaining
		}
		clock.Advance(advance)
		if clock.Now().Before(absolute.AbsoluteExpiresAt) {
			_, err = manager.Authenticate(ctx, "", absolute.ID, absolute.CSRFToken, true)
			require.NoError(t, err)
		}
	}
	manager.Sweep(ctx)
	assertChannelClosed(t, absolute.Done)
}

func TestSessionCapacityRejectsBeforeEntropyAndReleasesOnLogout(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	clock := testutil.NewFakeClock(testNow)
	service := NewService(store, clock, newDeterministicEntropy())
	sink := new(memorySink)
	_, err := service.Initialize(ctx, sink)
	require.NoError(t, err)
	limit, ok := contract.FixedLimitByName("admin_sessions")
	require.True(t, ok)
	entropy := newSessionEntropy(int(limit.Maximum + 1))
	manager := NewSessionManager(service, clock, entropy)
	t.Cleanup(manager.Shutdown)

	sessions := make([]CreatedSession, 0, limit.Maximum)
	for range limit.Maximum {
		created, err := manager.Exchange(ctx, sink.value)
		require.NoError(t, err)
		sessions = append(sessions, created)
	}
	assert.Equal(t, contract.LimitStatus{InUse: limit.Maximum, Limit: limit.Maximum, Saturated: true}, manager.Status())
	remaining := entropy.Remaining()
	_, err = manager.Exchange(ctx, sink.value)
	assert.ErrorIs(t, err, ErrSessionLimit)
	assert.Equal(t, remaining, entropy.Remaining())

	for _, session := range sessions {
		require.NoError(t, manager.Logout(session.ID))
		assertChannelClosed(t, session.Done)
	}
	assert.Equal(t, int64(0), manager.Status().InUse)
	_, err = manager.Exchange(ctx, sink.value)
	require.NoError(t, err)
}

func TestSessionRegistrySupportsConcurrentHighChurn(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	clock := testutil.NewFakeClock(testNow)
	service := NewService(store, clock, newDeterministicEntropy())
	sink := new(memorySink)
	_, err := service.Initialize(ctx, sink)
	require.NoError(t, err)
	const workers = 16
	const iterations = 16
	manager := NewSessionManager(service, clock, newSessionEntropy(workers*iterations))
	t.Cleanup(manager.Shutdown)

	errors := make(chan error, workers*iterations)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for range iterations {
				session, err := manager.Exchange(ctx, sink.value)
				if err == nil {
					err = manager.Logout(session.ID)
				}
				errors <- err
			}
		}()
	}
	group.Wait()
	close(errors)
	succeeded := 0
	for err := range errors {
		if err != nil {
			require.ErrorIs(t, err, ErrMutationBusy)
		} else {
			succeeded++
		}
	}
	assert.Positive(t, succeeded)
	assert.Equal(t, int64(0), manager.Status().InUse)
}

func TestCredentialInvalidationAndShutdownCloseBoundSessions(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	clock := testutil.NewFakeClock(testNow)
	service := NewService(store, clock, newDeterministicEntropy())
	initialSink := new(memorySink)
	initial, err := service.Initialize(ctx, initialSink)
	require.NoError(t, err)
	second, err := service.Create(ctx, nil)
	require.NoError(t, err)
	manager := NewSessionManager(service, clock, newSessionEntropy(6))

	initialSession, err := manager.Exchange(ctx, initialSink.value)
	require.NoError(t, err)
	secondSession, err := manager.Exchange(ctx, second.Bearer)
	require.NoError(t, err)
	require.NoError(t, service.Revoke(ctx, initial.ID))
	assertChannelClosed(t, initialSession.Done)
	assertChannelOpen(t, secondSession.Done)

	resetSink := new(memorySink)
	_, err = service.Reset(ctx, resetSink)
	require.NoError(t, err)
	assertChannelClosed(t, secondSession.Done)

	postReset, err := manager.Exchange(ctx, resetSink.value)
	require.NoError(t, err)
	manager.Shutdown()
	assertChannelClosed(t, postReset.Done)
	_, err = manager.Exchange(ctx, resetSink.value)
	assert.ErrorIs(t, err, ErrShuttingDown)
}

func TestSessionAuthenticationRejectsAmbiguityAndParentExpiry(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	clock := testutil.NewFakeClock(testNow)
	service := NewService(store, clock, newDeterministicEntropy())
	require.NoError(t, mustInitialize(service, ctx))
	expiresAt := testNow.Add(contract.CredentialMinimumLifetime)
	expiring, err := service.Create(ctx, &expiresAt)
	require.NoError(t, err)
	manager := NewSessionManager(service, clock, newSessionEntropy(2))
	t.Cleanup(manager.Shutdown)
	session, err := manager.Exchange(ctx, expiring.Bearer)
	require.NoError(t, err)

	_, err = manager.Authenticate(ctx, expiring.Bearer, session.ID, session.CSRFToken, true)
	assert.ErrorIs(t, err, ErrAmbiguousCredentials)
	clock.Advance(contract.CredentialMinimumLifetime)
	manager.Sweep(ctx)
	assertChannelClosed(t, session.Done)

	restarted := NewSessionManager(service, clock, newSessionEntropy(1))
	t.Cleanup(restarted.Shutdown)
	_, err = restarted.Authenticate(ctx, "", session.ID, session.CSRFToken, true)
	assert.ErrorIs(t, err, ErrAuthenticationRequired)
}

func newSessionEntropy(sessionCount int) *testutil.FakeEntropy {
	bytes := make([]byte, sessionCount*64)
	state := uint64(0xd1b54a32d192ed03)
	for index := range bytes {
		state ^= state << 7
		state ^= state >> 9
		state ^= state << 8
		bytes[index] = byte(state)
	}
	return testutil.NewFakeEntropy(bytes)
}

func assertChannelClosed(t *testing.T, channel <-chan struct{}) {
	t.Helper()
	select {
	case <-channel:
	default:
		t.Fatal("channel is open")
	}
}

func assertChannelOpen(t *testing.T, channel <-chan struct{}) {
	t.Helper()
	select {
	case <-channel:
		t.Fatal("channel is closed")
	default:
	}
}
