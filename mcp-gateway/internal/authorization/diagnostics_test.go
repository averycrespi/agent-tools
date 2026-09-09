package authorization

import (
	"testing"
	"testing/synctest"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

type authorityDiagnosticRecorder struct{ events chan diagnostics.Facts }

func (recorder *authorityDiagnosticRecorder) DebugEnabled() bool { return true }
func (recorder *authorityDiagnosticRecorder) Authority(facts diagnostics.Facts) {
	recorder.events <- facts
}

func TestAuthorityDiagnosticHandoffSnapshots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := newAuthorityRegistry(nil)
		recorder := &authorityDiagnosticRecorder{events: make(chan diagnostics.Facts, 256)}
		registry.diagnostics = recorder
		first, err := registry.acquire(t.Context())
		require.NoError(t, err)
		acquired := make(chan func(), 31)
		for range 31 {
			go func() { release, err := registry.acquire(t.Context()); require.NoError(t, err); acquired <- release }()
		}
		synctest.Wait()
		owned, waiting := registry.authorityOccupancy()
		require.Equal(t, 1, owned)
		require.Equal(t, 31, waiting)
		_, err = registry.acquire(t.Context())
		require.ErrorIs(t, err, ErrResourceLimit)
		// With the first owner held, every wait/reject sample sees one real owner,
		// not a placeholder, and none of the callers can have executed early.
		require.Empty(t, acquired)
		for len(recorder.events) > 0 {
			event := <-recorder.events
			require.Equal(t, 1, event.Owned)
			require.GreaterOrEqual(t, event.Waiting, 0)
			require.LessOrEqual(t, event.Waiting, 31)
			if event.Event == diagnostics.AuthorityReject {
				require.Equal(t, 31, event.Waiting)
			}
		}
		first()
		for remaining := 30; remaining >= 0; remaining-- {
			synctest.Wait()
			require.Len(t, acquired, 1, "exactly one executor after each handoff")
			owned, waiting = registry.authorityOccupancy()
			require.Equal(t, 1, owned)
			require.Equal(t, remaining, waiting)
			for len(recorder.events) > 0 {
				event := <-recorder.events
				// A release sample may fall before or after the next sender acquires.
				// In both cases the retiring work is gone in the same atomic snapshot.
				require.Equal(t, remaining+1, event.Owned+event.Waiting)
				require.GreaterOrEqual(t, event.Owned, 0)
				require.LessOrEqual(t, event.Owned, 1)
				require.GreaterOrEqual(t, event.Waiting, 0)
				require.LessOrEqual(t, event.Waiting, 31)
				if event.Event == diagnostics.AuthorityAcquire {
					require.Equal(t, 1, event.Owned)
					require.Equal(t, remaining, event.Waiting)
				}
			}
			release := <-acquired
			release()
		}
		synctest.Wait()
		owned, waiting = registry.authorityOccupancy()
		require.Zero(t, owned)
		require.Zero(t, waiting)
		event := <-recorder.events
		require.Equal(t, diagnostics.AuthorityRelease, event.Event)
		require.Zero(t, event.Owned)
		require.Zero(t, event.Waiting)
		require.Empty(t, recorder.events)
	})
}
