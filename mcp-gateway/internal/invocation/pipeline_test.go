package invocation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS5DrainPipelineFenceCountsWithoutQueueingAndJoinsAfterDeadline(t *testing.T) {
	fence := NewPipelineFence()
	firstRelease, ok := fence.TryEnter()
	require.True(t, ok)
	secondRelease, ok := fence.TryEnter()
	require.True(t, ok)
	fence.BeginDrain()
	_, ok = fence.TryEnter()
	assert.False(t, ok)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	drained := make(chan error, 1)
	go func() { drained <- fence.Drain(ctx) }()
	firstRelease()
	select {
	case <-drained:
		t.Fatal("drain returned while a pipeline remained active")
	default:
	}
	firstRelease()
	secondRelease()
	assert.NoError(t, <-drained)
	assert.NoError(t, fence.Drain(ctx))
}

func TestPipelineFenceHonorsCanceledDrainContext(t *testing.T) {
	fence := NewPipelineFence()
	release, ok := fence.TryEnter()
	require.True(t, ok)
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, fence.Drain(ctx), context.Canceled)
	release()
	assert.NoError(t, fence.Drain(context.Background()))
}
