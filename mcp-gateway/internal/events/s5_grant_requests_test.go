package events

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

func TestS5GrantRequestInvalidationsPreserveOrderAndOverflowToSnapshotRecovery(t *testing.T) {
	hub := New()
	t.Cleanup(hub.Shutdown)
	subscription, err := hub.Subscribe("credential", nil)
	require.NoError(t, err)
	requestEvent := contract.Invalidation{Kind: contract.InvalidationGrantRequests}
	authorizationEvent := contract.Invalidation{Kind: contract.InvalidationAuthorization}
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
}
