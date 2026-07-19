package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/cmd"
)

func TestWTSDHelpAndInvalidArgumentsNeverStartDaemon(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--unknown"}, {"unexpected"}} {
		calls := 0
		err := cmd.ExecuteWTSD(context.Background(), func(context.Context) error {
			calls++
			return nil
		}, nil, nil, args)
		if len(args) == 1 && args[0] == "--help" {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
		require.Zero(t, calls)
	}
}

func TestWTSDNoArgumentsStartsDaemonOnce(t *testing.T) {
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	err := cmd.ExecuteWTSD(ctx, func(runCtx context.Context) error {
		calls++
		cancel()
		<-runCtx.Done()
		return nil
	}, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
}
