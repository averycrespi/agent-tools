package exec

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOSRunner_Run_Success(t *testing.T) {
	r := NewOSRunner()
	out, err := r.Run(context.Background(), "echo", "hello")
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(out))
}

func TestOSRunner_Run_Error(t *testing.T) {
	r := NewOSRunner()
	_, err := r.Run(context.Background(), "false")
	assert.Error(t, err)
}

func TestOSRunner_Run_ContextCancellation(t *testing.T) {
	// Cancelling the context must terminate the subprocess. Without
	// CommandContext, `sleep 30` would outlive the test.
	r := NewOSRunner()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, "sleep", "30")
		done <- err
	}()

	// Give the subprocess a beat to start, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		assert.Error(t, err, "expected non-nil error after context cancel")
	case <-time.After(2 * time.Second):
		t.Fatal("subprocess did not exit within 2s after context cancel")
	}
}
