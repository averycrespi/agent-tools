package admin

import (
	"bytes"
	"context"
	"errors"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialCreateReadExpiryAndAuthenticationDomains(t *testing.T) {
	ctx := context.Background()
	store, databasePath := newStore(t)
	clock := testutil.NewFakeClock(testNow)
	service := NewService(store, clock, newDeterministicEntropy())
	require.NoError(t, mustInitialize(service, ctx))

	tooSoon := testNow.Add(contract.CredentialMinimumLifetime - time.Nanosecond)
	_, err := service.Create(ctx, &tooSoon)
	assert.ErrorIs(t, err, ErrInvalidExpiry)
	tooLate := testNow.Add(contract.CredentialMaximumLifetime + time.Nanosecond)
	_, err = service.Create(ctx, &tooLate)
	assert.ErrorIs(t, err, ErrInvalidExpiry)

	expiresAt := testNow.Add(contract.CredentialMinimumLifetime)
	created, err := service.Create(ctx, &expiresAt)
	require.NoError(t, err)
	assert.NotEmpty(t, created.Bearer)
	assert.Equal(t, contract.CredentialActive, created.Status)
	assert.False(t, created.NonExpiring)
	assert.Equal(t, expiresAt.Format(time.RFC3339Nano), *created.ExpiresAt)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{16}$`), created.Fingerprint)
	assert.Equal(t, "2", created.Revision)

	metadata, err := service.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.AdminCredential, metadata)
	items, err := service.List(ctx)
	require.NoError(t, err)
	assert.Len(t, items, 2)
	database, err := os.ReadFile(databasePath)
	require.NoError(t, err)
	assert.NotContains(t, string(database), created.Bearer)

	_, err = service.Authenticate(ctx, contract.AgentBearerPrefix+"candidate")
	assert.ErrorIs(t, err, ErrCredentialDomainMismatch)
	_, err = service.Authenticate(ctx, contract.AdminBearerPrefix+"unknown")
	assert.ErrorIs(t, err, ErrAuthenticationRequired)
	_, err = service.Authenticate(ctx, created.Bearer)
	require.NoError(t, err)

	clock.Advance(contract.CredentialMinimumLifetime)
	_, err = service.Authenticate(ctx, created.Bearer)
	assert.ErrorIs(t, err, ErrAuthenticationRequired)
	expired, err := service.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.CredentialExpired, expired.Status)
}

func TestRevocationPreservesLastActiveNonExpiringAuthorityUnderRace(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	service := NewService(store, testutil.NewFakeClock(testNow), newDeterministicEntropy())
	initialSink := new(memorySink)
	initial, err := service.Initialize(ctx, initialSink)
	require.NoError(t, err)
	second, err := service.Create(ctx, nil)
	require.NoError(t, err)

	start := make(chan struct{})
	errorsByID := make(map[string]error)
	var mutex sync.Mutex
	var group sync.WaitGroup
	for _, id := range []string{initial.ID, second.ID} {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			err := service.Revoke(ctx, id)
			mutex.Lock()
			errorsByID[id] = err
			mutex.Unlock()
		}()
	}
	close(start)
	group.Wait()

	items, err := service.List(ctx)
	require.NoError(t, err)
	activeNonExpiring := 0
	for _, item := range items {
		if item.Status == contract.CredentialActive && item.NonExpiring {
			activeNonExpiring++
		}
	}
	assert.Equal(t, 1, activeNonExpiring)
	for _, err := range errorsByID {
		assert.True(t, err == nil || errors.Is(err, ErrLastNonExpiring) || errors.Is(err, ErrMutationBusy))
	}

	for _, item := range items {
		if item.Status == contract.CredentialActive {
			assert.ErrorIs(t, service.Revoke(ctx, item.ID), ErrLastNonExpiring)
		}
	}
}

func TestCredentialCapPrunesOldestTerminalRecord(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	clock := testutil.NewFakeClock(testNow)
	service := NewService(store, clock, newDeterministicEntropy())
	initialSink := new(memorySink)
	initial, err := service.Initialize(ctx, initialSink)
	require.NoError(t, err)

	limit, ok := contract.FixedLimitByName("admin_credentials")
	require.True(t, ok)
	for index := int64(1); index < limit.Maximum; index++ {
		clock.Advance(time.Millisecond)
		_, err := service.Create(ctx, nil)
		require.NoError(t, err)
	}
	_, err = service.Create(ctx, nil)
	assert.ErrorIs(t, err, ErrResourceLimit)

	require.NoError(t, service.Revoke(ctx, initial.ID))
	clock.Advance(time.Millisecond)
	replacement, err := service.Create(ctx, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, replacement.Bearer)
	_, err = service.Get(ctx, initial.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	items, err := service.List(ctx)
	require.NoError(t, err)
	assert.Len(t, items, int(limit.Maximum))

	resetSink := new(memorySink)
	reset, err := service.Reset(ctx, resetSink)
	require.NoError(t, err)
	assert.NotEmpty(t, resetSink.value)
	_, err = service.Authenticate(ctx, replacement.Bearer)
	assert.ErrorIs(t, err, ErrAuthenticationRequired)
	_, err = service.Authenticate(ctx, resetSink.value)
	require.NoError(t, err)
	items, err = service.List(ctx)
	require.NoError(t, err)
	assert.Len(t, items, int(limit.Maximum))
	active := 0
	for _, item := range items {
		if item.Status == contract.CredentialActive {
			active++
			assert.Equal(t, reset.ID, item.ID)
		}
	}
	assert.Equal(t, 1, active)
}

func newDeterministicEntropy() *testutil.FakeEntropy {
	value := make([]byte, 42*140)
	state := uint64(0x9e3779b97f4a7c15)
	for index := range value {
		state ^= state << 7
		state ^= state >> 9
		state ^= state << 8
		value[index] = byte(state)
	}
	return testutil.NewFakeEntropy(bytes.Clone(value))
}
