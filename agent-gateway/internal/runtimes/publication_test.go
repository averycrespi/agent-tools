package runtimes

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStdioStopJoinsExitPublication(t *testing.T) {
	started := make(chan struct{})
	timer := make(chan time.Time)
	finished := make(chan struct{})
	runtime := &StdioRuntime{exited: true, finished: finished, command: exec.Command("fixture"), supervisor: &StdioSupervisor{after: func(time.Duration) <-chan time.Time { close(started); return timer }}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	stopped := make(chan bool, 1)
	go func() { stopped <- runtime.Stop(ctx) }()
	select {
	case <-started:
	case <-stopped:
		t.Fatal("process exit incorrectly qualified unpublished detail")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-stopped:
		t.Fatal("cleanup did not join publication")
	default:
	}
	close(finished)
	require.True(t, <-stopped)
}
