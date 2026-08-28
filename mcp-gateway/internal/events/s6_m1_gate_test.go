package events

import (
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6M1Gate(t *testing.T) {
	hub := New()
	terminal := make(chan struct{})
	subscription, err := hub.Subscribe("browser-parent", terminal)
	require.NoError(t, err)
	event := contract.Invalidation{Kind: contract.InvalidationSystemStatus}
	hub.Publish(event)
	assert.Equal(t, event, <-subscription.Events())

	close(terminal)
	select {
	case <-subscription.Done():
	case <-time.After(time.Second):
		t.Fatal("terminal browser session did not close the shared stream")
	}
	assert.Equal(t, int64(0), hub.Status().InUse)

	hub.Shutdown()
	_, err = hub.Subscribe("late", nil)
	assert.ErrorIs(t, err, ErrShuttingDown)
}
