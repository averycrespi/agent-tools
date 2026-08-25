package events

import (
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHubIsBoundedBestEffortAndHasNoReplay(t *testing.T) {
	hub := New()
	t.Cleanup(hub.Shutdown)
	hub.Publish(contract.Invalidation{Kind: contract.InvalidationSystemStatus})

	subscriptions := make([]*Subscription, 0, 16)
	for range 16 {
		subscription, err := hub.Subscribe("credential", nil)
		require.NoError(t, err)
		subscriptions = append(subscriptions, subscription)
	}
	_, err := hub.Subscribe("credential", nil)
	assert.ErrorIs(t, err, ErrStreamLimit)
	assert.Equal(t, contract.LimitStatus{InUse: 16, Limit: 16, Saturated: true}, hub.Status())
	select {
	case <-subscriptions[0].Events():
		t.Fatal("a new subscription replayed prior state")
	default:
	}

	event := contract.Invalidation{Kind: contract.InvalidationBackups}
	for range 16 {
		hub.Publish(event)
	}
	hub.Publish(event)
	assertChannelClosed(t, subscriptions[0].Done())
	assert.Equal(t, int64(0), hub.Status().InUse)

	reconnected, err := hub.Subscribe("credential", nil)
	require.NoError(t, err)
	authorization := contract.Invalidation{Kind: contract.InvalidationAuthorization}
	hub.Publish(authorization)
	assert.Equal(t, authorization, <-reconnected.Events())
}

func TestHubClosesOnlyBoundAuthorityAndTerminalSession(t *testing.T) {
	hub := New()
	t.Cleanup(hub.Shutdown)
	terminal := make(chan struct{})
	first, err := hub.Subscribe("first", terminal)
	require.NoError(t, err)
	second, err := hub.Subscribe("second", nil)
	require.NoError(t, err)

	id := "first"
	hub.InvalidateCredential(&id)
	assertChannelClosed(t, first.Done())
	assertChannelOpen(t, second.Done())

	thirdTerminal := make(chan struct{})
	third, err := hub.Subscribe("third", thirdTerminal)
	require.NoError(t, err)
	close(thirdTerminal)
	select {
	case <-third.Done():
	case <-time.After(time.Second):
		t.Fatal("terminal session did not close its event stream")
	}

	hub.InvalidateCredential(nil)
	assertChannelClosed(t, second.Done())
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
