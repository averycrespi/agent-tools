package invocation

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTrafficVisibleTerminalPrecedesWriterSettlement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var block atomic.Bool
	committed, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	traffic, owner := trafficFixture(t, nil, func(point string) error {
		if point == "acknowledgment" && block.Load() {
			close(committed)
			<-release
		}
		return nil
	})
	t.Cleanup(unblock)
	observation := recordMCP(t, traffic, trafficPrepared(1))
	block.Store(true)
	require.NoError(t, traffic.ObserveMCPCompletion(observation, trafficCompletion(), nil))
	select {
	case <-committed:
	case <-ctx.Done():
		t.Fatal("terminal did not reach the post-commit barrier")
	}
	history, err := traffic.History(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	require.NotNil(t, history.Records[0].CompletedAt)
	// Readable rows are not writer-settlement evidence.
	require.False(t, traffic.writerGate.TryLock())
	unblock()
	require.NoError(t, traffic.Close())
	reopened, err := openTraffic(ctx, owner, invocationTestInstallationID, invocationID(90), traffic.config, false, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	history, err = reopened.History(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	require.NotNil(t, history.Records[0].CompletedAt, "settled terminal survives restart")
}
