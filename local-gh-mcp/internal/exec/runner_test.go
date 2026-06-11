package exec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOSRunner_ImplementsRunner(t *testing.T) {
	var _ Runner = &OSRunner{}
}

func TestOSRunner_Run(t *testing.T) {
	r := NewOSRunner()
	out, err := r.Run(context.Background(), "echo", "hello")
	assert.NoError(t, err)
	assert.Contains(t, string(out), "hello")
}

type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestTimeoutRunner_RunReturnsWhenCommandBlocks(t *testing.T) {
	r := NewTimeoutRunner(blockingRunner{}, 10*time.Millisecond)
	start := time.Now()

	_, err := r.Run(context.Background(), "gh", "api", "/user")

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "timed out")
	assert.Less(t, time.Since(start), time.Second)
}

func TestTimeoutRunner_PropagatesInnerError(t *testing.T) {
	want := errors.New("boom")
	r := NewTimeoutRunner(errorRunner{err: want}, time.Second)

	_, err := r.Run(context.Background(), "gh")

	assert.ErrorIs(t, err, want)
}

type errorRunner struct{ err error }

func (r errorRunner) Run(context.Context, string, ...string) ([]byte, error) { return nil, r.err }
