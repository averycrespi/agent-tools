package events

import (
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6PostEvents(t *testing.T) {
	hub := New()
	t.Cleanup(hub.Shutdown)
	getSubscription, err := hub.Subscribe("get-credential", nil)
	require.NoError(t, err)
	postSessionDone := make(chan struct{})
	postSubscription, err := hub.Subscribe("post-credential", postSessionDone)
	require.NoError(t, err)

	event := contract.Invalidation{Kind: contract.InvalidationSystemStatus}
	hub.Publish(event)
	assert.Equal(t, event, <-getSubscription.Events())
	assert.Equal(t, event, <-postSubscription.Events())

	close(postSessionDone)
	select {
	case <-postSubscription.Done():
	case <-time.After(time.Second):
		t.Fatal("session closure did not close POST subscription")
	}
	assertChannelOpen(t, getSubscription.Done())
	getSubscription.Close()
	overflow, err := hub.Subscribe("overflow", nil)
	require.NoError(t, err)
	for range hub.buffer + 1 {
		hub.Publish(event)
	}
	assertChannelClosed(t, overflow.Done())
	assert.Equal(t, int64(0), hub.Status().InUse)
}
