package invocation

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

func TestTrafficFailedMixedBatchExplainsLossWithoutReplay(t *testing.T) {
	for _, phase := range []string{"statement", "acknowledgment"} {
		t.Run(phase, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			var batches atomic.Int64
			s, _ := trafficFixture(t, nil, func(point string) error {
				if point == "before_begin" && batches.Add(1) == 1 {
					close(entered)
					<-release
				}
				if batches.Load() == 2 && point == phase {
					return errors.New("injected durable writer failure")
				}
				return nil
			})
			var output bytes.Buffer
			adapter := diagnostics.New(&output, diagnostics.Warn)
			t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
			s.SetTrafficDiagnostics(adapter)
			require.NotNil(t, s.ObserveMCP(trafficPrepared(99)))
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("writer did not enter")
			}
			mcp := s.ObserveMCP(trafficPrepared(1))
			require.NotNil(t, mcp)
			require.NoError(t, s.ObserveMCPCompletion(mcp, trafficCompletion(), nil))
			http := s.ObserveHTTP(httpTrafficAdmission(2))
			require.NotNil(t, http)
			require.NoError(t, s.ObserveHTTPCompletion(http, httpTrafficCompletion()))
			git := s.ObserveGit(gitTrafficAdmission(3))
			require.NotNil(t, git)
			require.NoError(t, s.ObserveGitCompletion(git, gitTrafficCompletion()))
			unblock()
			waitTraffic(t, s)
			require.EqualValues(t, 2, batches.Load())
			require.NoError(t, s.Close())
			require.True(t, adapter.Finish(nil))
			require.Contains(t, output.String(), "batch_observations=6 admissions=3 completions=3 mcp=2 http=2 git=2")
			require.Contains(t, output.String(), "application_dispatch=unaffected")
			require.Contains(t, output.String(), "injected durable writer failure")
			if phase == "statement" {
				require.Contains(t, output.String(), "persistence=not_persisted")
			} else {
				require.Contains(t, output.String(), "persistence=not_acknowledged")
			}
		})
	}
}
