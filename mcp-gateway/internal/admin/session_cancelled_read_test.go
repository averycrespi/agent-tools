package admin

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCancelledSessionReadRecognizesRollbackRace(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rolledBack := fmt.Errorf("commit storage view: %w", sql.ErrTxDone)
	require.True(t, cancelledSessionRead(ctx, rolledBack))
	require.False(t, cancelledSessionRead(t.Context(), rolledBack))
	require.False(t, cancelledSessionRead(ctx, nil))
}

func TestCancelledSessionReadDoesNotRevokeLiveSession(t *testing.T) {
	for _, operation := range []string{"authenticate", "bootstrap", "sweep"} {
		for _, cancellation := range []string{"cancelled", "deadline"} {
			t.Run(operation+"/"+cancellation, func(t *testing.T) {
				store, _ := newStore(t)
				clock := testutil.NewFakeClock(testNow)
				service := NewService(store, clock, newDeterministicEntropy())
				sink := new(memorySink)
				_, err := service.Initialize(t.Context(), sink)
				require.NoError(t, err)
				manager := NewSessionManager(service, clock, newSessionEntropy(1))
				t.Cleanup(manager.Shutdown)
				session, err := manager.Exchange(t.Context(), sink.value)
				require.NoError(t, err)
				interrupted, cancel := context.WithCancel(t.Context())
				cancel()
				expected := context.Canceled
				if cancellation == "deadline" {
					interrupted, cancel = context.WithDeadline(t.Context(), time.Unix(0, 0))
					defer cancel()
					expected = context.DeadlineExceeded
				}
				switch operation {
				case "authenticate":
					credential, readErr := manager.Authenticate(interrupted, "", session.ID, session.CSRFToken, true)
					require.ErrorIs(t, readErr, expected)
					require.Empty(t, credential.ID, "cancelled read must not authenticate")
				case "bootstrap":
					result, readErr := manager.Bootstrap(interrupted, session.ID)
					require.ErrorIs(t, readErr, expected)
					require.Empty(t, result.ID, "cancelled read must not bootstrap")
				case "sweep":
					manager.Sweep(interrupted)
				}
				select {
				case <-session.Done:
					t.Fatal("cancelled read closed the live session")
				default:
				}
				_, err = manager.Authenticate(t.Context(), "", session.ID, "wrong", true)
				require.ErrorIs(t, err, ErrCSRF)
				_, err = manager.Authenticate(t.Context(), "", session.ID, session.CSRFToken, true)
				require.NoError(t, err, "a cancelled read must not revoke unrelated live session authority")
			})
		}
	}
}
