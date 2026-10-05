package invocation

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/activity"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/stretchr/testify/require"
)

func trafficFixture(t *testing.T, configure func(*TrafficConfig), fault func(string) error) (*TrafficStore, *gatewaypaths.Ownership) {
	t.Helper()
	owner, err := gatewaypaths.Acquire(filepath.Join(t.TempDir(), "installation"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	config := DefaultTrafficConfig()
	config.BudgetBytes = 8 << 20
	if configure != nil {
		configure(&config)
	}
	s, err := openTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), config, true, fault)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s, owner
}
func trafficPrepared(id int) PreparedAdmission {
	return PreparedAdmission{Identity: activity.Identity{InvocationID: invocationID(id), AdmittedAt: canonicalInvocationTime(invocationTestTime)}, admission: testEvaluatedAdmission()}
}
func trafficCompletion() activity.Completion {
	return activity.Completion{CompletedAt: canonicalInvocationTime(invocationTestTime.Add(time.Second)), Class: contract.TerminalSucceeded}
}

// Settlement is a test-only persistence barrier, never runtime admission authority.
func waitTraffic(t *testing.T, s *TrafficStore) {
	t.Helper()
	require.Eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.queued == 0 }, 5*time.Second, time.Millisecond)
	s.writerGate.Lock()
	defer s.writerGate.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Zero(t, s.queued)
}
func recordMCP(t *testing.T, s *TrafficStore, p PreparedAdmission) *TrafficObservation {
	t.Helper()
	o := s.ObserveMCP(p)
	require.NotNil(t, o)
	waitTraffic(t, s)
	require.True(t, s.Healthy())
	return o
}
func recordHTTP(t *testing.T, s *TrafficStore, a contract.HTTPTrafficAdmission) *TrafficObservation {
	t.Helper()
	o := s.ObserveHTTP(a)
	require.NotNil(t, o)
	waitTraffic(t, s)
	require.True(t, s.Healthy())
	return o
}
func recordGit(t *testing.T, s *TrafficStore, a contract.GitTrafficAdmission) *TrafficObservation {
	t.Helper()
	o := s.ObserveGit(a)
	require.NotNil(t, o)
	waitTraffic(t, s)
	require.True(t, s.Healthy())
	return o
}
func recordMCPCompletion(t *testing.T, s *TrafficStore, o *TrafficObservation, c activity.Completion) {
	t.Helper()
	require.NoError(t, s.ObserveMCPCompletion(o, c, nil))
	waitTraffic(t, s)
}
func recordHTTPCompletion(t *testing.T, s *TrafficStore, o *TrafficObservation, c contract.HTTPTrafficCompletion) {
	t.Helper()
	require.NoError(t, s.ObserveHTTPCompletion(o, c))
	waitTraffic(t, s)
}
func recordGitCompletion(t *testing.T, s *TrafficStore, o *TrafficObservation, c contract.GitTrafficCompletion) {
	t.Helper()
	require.NoError(t, s.ObserveGitCompletion(o, c))
	waitTraffic(t, s)
}

func TestTrafficSnapshotsAreImmutableAndLateStartsCannotRegress(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	ring, clock := recordedFixture()
	s.recorded = ring
	p := trafficPrepared(1)
	o := recordMCP(t, s, p)
	p.admission.MCP.RedactedArguments[0] = 'x'
	recordMCPCompletion(t, s, o, trafficCompletion())
	// Duplicate completion and late start are observations, not dispatch permissions.
	recordMCPCompletion(t, s, o, trafficCompletion())
	s.observeInitial(o)
	waitTraffic(t, s)
	history, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	require.True(t, validStoredInvocation(history.Records[0]))
	require.Equal(t, contract.TerminalSucceeded, *history.Records[0].TerminalClass)
	clock.advance(time.Minute)
	counts := s.RecordedActivity().Buckets[14].Counts.MCP
	require.EqualValues(t, 1, counts.Admissions.Allow)
	require.EqualValues(t, 1, counts.Completions.Succeeded)
}

func TestTrafficTerminalInsertsWhenInitialWasDropped(t *testing.T) {
	for _, protocol := range []string{"mcp", "http", "git"} {
		t.Run(protocol, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			s, _ := trafficFixture(t, func(c *TrafficConfig) { c.QueueRecords = 1; c.BatchRecords = 1 }, func(point string) error {
				if point == "before_begin" {
					once.Do(func() { close(entered); <-release })
				}
				return nil
			})
			require.NotNil(t, s.ObserveMCP(trafficPrepared(9)))
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("writer did not enter")
			}
			var o *TrafficObservation
			switch protocol {
			case "mcp":
				o = s.ObserveMCP(trafficPrepared(1))
			case "http":
				o = s.ObserveHTTP(httpTrafficAdmission(1))
			case "git":
				o = s.ObserveGit(gitTrafficAdmission(1))
			}
			require.NotNil(t, o, "lost start still provides sanitized terminal snapshot")
			unblock()
			waitTraffic(t, s)
			switch protocol {
			case "mcp":
				recordMCPCompletion(t, s, o, trafficCompletion())
				h, err := s.History(t.Context(), 0, 10)
				require.NoError(t, err)
				require.Len(t, h.Records, 2)
				require.NotNil(t, h.Records[1].TerminalClass)
			case "http":
				recordHTTPCompletion(t, s, o, httpTrafficCompletion())
				h, err := s.HTTPHistory(t.Context(), 0, 10)
				require.NoError(t, err)
				require.Len(t, h.Records, 1)
				require.NotNil(t, h.Records[0].Completion)
			case "git":
				recordGitCompletion(t, s, o, gitTrafficCompletion())
				h, err := s.GitHistory(t.Context(), 0, 10)
				require.NoError(t, err)
				require.Len(t, h.Records, 1)
				require.NotNil(t, h.Records[0].Completion)
			}
			s.observeInitial(o)
			waitTraffic(t, s)
			require.True(t, s.Healthy())
		})
	}
}

func TestTrafficUncertaintyFaultsOnlyRecorderAndNeverReplays(t *testing.T) {
	for _, point := range []string{"before_begin", "statement", "commit", "rollback", "acknowledgment"} {
		t.Run(point, func(t *testing.T) {
			s, owner := trafficFixture(t, nil, func(at string) error {
				if at == point || point == "rollback" && at == "statement" {
					return errors.New("I/O uncertainty")
				}
				return nil
			})
			o := s.ObserveMCP(trafficPrepared(1))
			require.NotNil(t, o)
			waitTraffic(t, s)
			require.False(t, s.Healthy())
			require.ErrorIs(t, s.ObserveMCPCompletion(o, trafficCompletion(), nil), ErrTrafficFault)
			require.NotNil(t, s.ObserveMCP(trafficPrepared(2)))
			h, err := s.History(t.Context(), 0, 10)
			require.NoError(t, err)
			expected := 0
			if point == "acknowledgment" {
				expected = 1
			}
			require.Len(t, h.Records, expected)
			require.NoError(t, s.Close())
			reopened, err := OpenTraffic(context.Background(), owner, invocationTestInstallationID, invocationID(90), s.config)
			require.NoError(t, err)
			defer func() { require.NoError(t, reopened.Close()) }()
			h, err = reopened.History(t.Context(), 0, 10)
			require.NoError(t, err)
			require.Len(t, h.Records, expected)
			for _, row := range h.Records {
				require.Nil(t, row.TerminalClass)
			}
		})
	}
}
