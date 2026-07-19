package state_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
)

func TestLockHonorsContextAndSerializes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.lock")
	first, err := state.Acquire(context.Background(), path)
	require.NoError(t, err)
	defer func() { require.NoError(t, first.Unlock()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = state.Acquire(ctx, path)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestDecodeActionKeyRejectsMissingRequiredFields(t *testing.T) {
	_, err := state.DecodeActionKey(`{}`)
	require.ErrorContains(t, err, "requires")
}

func TestLedgerSuppressesSameAttemptAndAllowsChangedDigestOrRerun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.json")
	ledger, err := state.LoadLedger(path)
	require.NoError(t, err)
	key := state.ActionKey{Repository: "r", Worktree: "w", Trigger: "passive", Digest: "one"}
	require.True(t, ledger.Eligible(key))
	ledger.Record(key, state.ActionResult{Success: false, Error: "failed"})
	require.False(t, ledger.Eligible(key))
	require.True(t, ledger.Eligible(state.ActionKey{Repository: "r", Worktree: "w", Trigger: "passive", Digest: "two"}))
	ledger.Rerun(key)
	require.True(t, ledger.Eligible(key))
	require.NoError(t, ledger.Save(path))

	reloaded, err := state.LoadLedger(path)
	require.NoError(t, err)
	require.True(t, reloaded.Eligible(key))
}

func TestCommitUncertainRecognizesWrappedPostCommitError(t *testing.T) {
	cause := errors.New("sync failed")
	err := fmt.Errorf("saving ledger: %w", &state.PostCommitError{Err: cause})
	require.True(t, state.CommitUncertain(err))
	require.ErrorIs(t, err, cause)
	require.False(t, state.CommitUncertain(cause))
}
