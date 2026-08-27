package events

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

func TestS5EventsPreserveIDFreeOrderOverflowLossAndReconnect(t *testing.T) {
	hub := New()
	t.Cleanup(hub.Shutdown)
	subscription, err := hub.Subscribe("credential", nil)
	require.NoError(t, err)
	requestEvent := contract.Invalidation{Kind: contract.InvalidationGrantRequests}
	authorizationEvent := contract.Invalidation{Kind: contract.InvalidationAuthorization}
	assert.Nil(t, requestEvent.ResourceID)
	assert.Nil(t, authorizationEvent.ResourceID)
	hub.Publish(requestEvent)
	hub.Publish(authorizationEvent)
	assert.Equal(t, requestEvent, <-subscription.Events())
	assert.Equal(t, authorizationEvent, <-subscription.Events())
	for range 16 {
		hub.Publish(requestEvent)
	}
	hub.Publish(authorizationEvent)
	assertChannelClosed(t, subscription.Done())
	assert.Equal(t, int64(0), hub.Status().InUse)

	reconnected, err := hub.Subscribe("credential", nil)
	require.NoError(t, err)
	select {
	case <-reconnected.Events():
		t.Fatal("reconnected subscriber replayed a lost event")
	default:
	}
	fresh := contract.Invalidation{Kind: contract.InvalidationSystemStatus}
	hub.Publish(fresh)
	assert.Equal(t, fresh, <-reconnected.Events())
}
