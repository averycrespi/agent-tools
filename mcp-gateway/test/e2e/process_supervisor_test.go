//go:build e2e

package e2e

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestS5E2EProcessSupervisorAdoption(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	harness := newGatewayHarnessContext(t, ctx)
	harness.Start()
	cancel()

	result, waitErr := harness.process.Wait()
	harness.process = nil
	require.ErrorIs(t, waitErr, context.Canceled, "stderr: %s", result.Stderr)
	require.True(t, result.Cleanup.Reaped)
	require.False(t, result.Cleanup.Survived)
	require.Eventually(t, func() bool {
		connection, err := net.DialTimeout("tcp", harness.authority, 50*time.Millisecond)
		if connection != nil {
			_ = connection.Close()
		}
		return errors.Is(err, context.DeadlineExceeded) || err != nil
	}, time.Second, 10*time.Millisecond)
}
