package broker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stubApprover is a simple approver for MultiApprover tests (no testify mock needed).
type stubApprover struct {
	approved     bool
	denialReason string
	err          error
	delay        time.Duration
}

func (s *stubApprover) Review(ctx context.Context, _ string, _ map[string]any) (bool, string, error) {
	select {
	case <-time.After(s.delay):
		return s.approved, s.denialReason, s.err
	case <-ctx.Done():
		return false, "", ctx.Err()
	}
}

func TestMultiApprover_FirstApproverWins(t *testing.T) {
	fast := &stubApprover{approved: true, delay: 0}
	slow := &stubApprover{approved: false, denialReason: "user", delay: 10 * time.Second}

	m := NewMultiApprover(30*time.Second, fast, slow)
	approved, reason, err := m.Review(context.Background(), "tool", nil)

	require.NoError(t, err)
	require.True(t, approved)
	require.Empty(t, reason)
}

func TestMultiApprover_ExplicitDenyPropagated(t *testing.T) {
	denier := &stubApprover{approved: false, denialReason: "user", delay: 0}

	m := NewMultiApprover(30*time.Second, denier)
	approved, reason, err := m.Review(context.Background(), "tool", nil)

	require.NoError(t, err)
	require.False(t, approved)
	require.Equal(t, "user", reason)
}

func TestMultiApprover_TimeoutReturnsDeniedWithReason(t *testing.T) {
	never := &stubApprover{delay: 10 * time.Second}

	m := NewMultiApprover(50*time.Millisecond, never)
	approved, reason, err := m.Review(context.Background(), "tool", nil)

	require.NoError(t, err)
	require.False(t, approved)
	require.Equal(t, "timeout", reason)
}

func TestMultiApprover_ContextCancelledApproverSkipped(t *testing.T) {
	// Approver returns a context error (simulates being cancelled by another approver winning)
	ctxErr := &stubApprover{err: errors.New("context canceled"), delay: 0}
	good := &stubApprover{approved: true, delay: 5 * time.Millisecond}

	m := NewMultiApprover(30*time.Second, ctxErr, good)
	approved, reason, err := m.Review(context.Background(), "tool", nil)

	require.NoError(t, err)
	require.True(t, approved)
	require.Empty(t, reason)
}
