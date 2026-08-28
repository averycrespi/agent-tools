package admin

import (
	"context"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6Bootstrap(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	clock := testutil.NewFakeClock(testNow)
	service := NewService(store, clock, newDeterministicEntropy())
	sink := new(memorySink)
	_, err := service.Initialize(ctx, sink)
	require.NoError(t, err)
	manager := NewSessionManager(service, clock, newSessionEntropy(2))
	t.Cleanup(manager.Shutdown)

	created, err := manager.Exchange(ctx, sink.value)
	require.NoError(t, err)
	clock.Advance(5 * time.Minute)
	bootstrapped, err := manager.Bootstrap(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, bootstrapped.ID)
	assert.Equal(t, created.CSRFToken, bootstrapped.CSRFToken)
	assert.Equal(t, created.AbsoluteExpiresAt, bootstrapped.AbsoluteExpiresAt)
	assert.Equal(t, clock.Now().Add(contract.AdminSessionIdleLifetime), bootstrapped.IdleExpiresAt)
	assert.Equal(t, created.Done, bootstrapped.Done)

	_, err = manager.Bootstrap(ctx, "unknown")
	assert.ErrorIs(t, err, ErrAuthenticationRequired)
	clock.Advance(contract.AdminSessionIdleLifetime + time.Second)
	_, err = manager.Bootstrap(ctx, created.ID)
	assert.ErrorIs(t, err, ErrAuthenticationRequired)
	assert.Equal(t, int64(0), manager.Status().InUse)
}
