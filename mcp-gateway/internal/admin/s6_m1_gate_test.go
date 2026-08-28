package admin

import (
	"context"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6M1Gate(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	clock := testutil.NewFakeClock(testNow)
	service := NewService(store, clock, newDeterministicEntropy())
	sink := new(memorySink)
	_, err := service.Initialize(ctx, sink)
	require.NoError(t, err)
	manager := NewSessionManager(service, clock, newSessionEntropy(2))

	created, err := manager.Exchange(ctx, sink.value)
	require.NoError(t, err)
	current, err := manager.Bootstrap(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.CSRFToken, current.CSRFToken)
	assert.Equal(t, int64(1), manager.Status().InUse)

	manager.Shutdown()
	select {
	case <-created.Done:
	default:
		t.Fatal("shutdown did not close the browser session")
	}
	_, err = manager.Bootstrap(ctx, created.ID)
	assert.ErrorIs(t, err, ErrAuthenticationRequired)
}
