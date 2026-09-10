package runtimes

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/require"
)

type diagnosticCatalog struct{}

func (diagnosticCatalog) Activate(context.Context, Candidate) CatalogOutcome {
	return CatalogOutcome{State: contract.ActiveCatalogCurrent}
}

func TestUpstreamRetryTracePreservesCappedBackoffAndRecovery(t *testing.T) {
	repository := newFakeRepository(1)
	reason := contract.ReasonConnectivity
	retry := Outcome{State: contract.RuntimeRetryWait, Reason: &reason, Retryable: true, DiagnosticPhase: diagnostics.PhaseConnection}
	driver := &outcomeDriver{calls: make(chan Candidate, 16)}
	for range 8 {
		driver.outcomes = append(driver.outcomes, retry)
	}
	driver.outcomes = append(driver.outcomes, activeOutcome())
	scheduler := newFakeScheduler()
	observer := &settlementObserver{}
	now := time.Unix(100, 0)
	manager, err := New(Options{Repository: repository, Driver: driver, Catalog: diagnosticCatalog{}, Scheduler: scheduler, Diagnostics: observer, DiagnosticNow: func() time.Time { return now }})
	require.NoError(t, err)
	t.Cleanup(manager.Shutdown)
	var id string
	for id = range repository.servers {
		break
	}
	manager.Trigger(id, nil, true)
	waitSettlementWorkers(t, manager)
	for index, expected := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute} {
		require.Equal(t, contract.RuntimeRetryWait, manager.Status(id).State)
		now = now.Add(expected)
		require.Equal(t, expected, scheduler.fire(t, index))
		waitSettlementWorkers(t, manager)
	}
	require.Equal(t, contract.RuntimeActive, manager.Status(id).State)
	require.Zero(t, manager.Status(id).RetryAttempt)
	attempts := map[uint64]bool{}
	var retries, completions, recovered int
	for _, facts := range observer.facts {
		require.NotZero(t, facts.Upstream)
		require.Equal(t, observer.facts[0].Upstream, facts.Upstream)
		switch facts.Event {
		case diagnostics.UpstreamAttemptStart:
			require.False(t, attempts[facts.Attempt])
			attempts[facts.Attempt] = true
		case diagnostics.UpstreamAttemptComplete:
			require.True(t, attempts[facts.Attempt])
			completions++
		case diagnostics.UpstreamRetryScheduled:
			retries++
			require.EqualValues(t, retries, facts.Retry)
			require.NotEqual(t, diagnostics.ReasonTimeout, facts.Reason)
		case diagnostics.UpstreamRecovered:
			recovered++
		}
	}
	require.Len(t, attempts, 9)
	require.Equal(t, 9, completions)
	require.Equal(t, 8, retries)
	require.Equal(t, 1, recovered)
}

type lifecycleDiagnosticSink struct {
	entered, release chan struct{}
	once             sync.Once
	broken           bool
}

func (sink *lifecycleDiagnosticSink) Write(data []byte) (int, error) {
	sink.once.Do(func() { close(sink.entered) })
	<-sink.release
	if sink.broken {
		return 0, errors.New("private sink failure")
	}
	return len(data), nil
}

func TestUpstreamLifecycleCompletesWithBlockedOrBrokenDiagnostics(t *testing.T) {
	for _, broken := range []bool{false, true} {
		sink := &lifecycleDiagnosticSink{entered: make(chan struct{}), release: make(chan struct{}), broken: broken}
		adapter := diagnostics.New(sink, diagnostics.Debug)
		t.Cleanup(func() {
			select {
			case <-sink.release:
			default:
				close(sink.release)
			}
			adapter.Finish(nil)
			<-adapter.Done()
		})
		adapter.Reconciliation(diagnostics.Facts{Event: diagnostics.UpstreamAttemptStart, Attempt: 1, Disposition: diagnostics.DispositionExecuting})
		select {
		case <-sink.entered:
		case <-time.After(time.Second):
			t.Fatal("sink was not entered")
		}
		for range diagnostics.QueueRecords + 2 {
			adapter.Reconciliation(diagnostics.Facts{Event: diagnostics.UpstreamAttemptStart, Attempt: 2, Disposition: diagnostics.DispositionExecuting})
		}
		repository := newFakeRepository(2)
		driver := &outcomeDriver{outcomes: []Outcome{activeOutcome(), activeOutcome()}, calls: make(chan Candidate, 2)}
		manager, err := New(Options{Repository: repository, Driver: driver, Catalog: diagnosticCatalog{}, Diagnostics: adapter})
		require.NoError(t, err)
		for id := range repository.servers {
			manager.Trigger(id, nil, true)
		}
		waitSettlementWorkers(t, manager)
		for id := range repository.servers {
			require.Equal(t, contract.RuntimeActive, manager.Status(id).State)
		}
		<-manager.Drain(t.Context())
		close(sink.release)
		require.Equal(t, !broken, adapter.Finish(nil))
		<-adapter.Done()
	}
}

type diagnosticOutcomeCatalog struct{ outcomeCatalog }

func (catalog diagnosticOutcomeCatalog) Refresh(context.Context, Candidate) CatalogOutcome {
	return catalog.outcome
}
func (diagnosticOutcomeCatalog) Withdraw(Candidate, contract.ActiveCatalogState) error { return nil }

func TestCatalogRetryEvidenceReachesActivationAndExplicitRefreshHealth(t *testing.T) {
	for name, delay := range map[string]time.Duration{"not_scheduled": 0, "scheduled": 30 * time.Second} {
		t.Run(name, func(t *testing.T) {
			repository := newFakeRepository(1)
			observer := &settlementObserver{}
			driver := &outcomeDriver{outcomes: []Outcome{activeOutcome()}, calls: make(chan Candidate, 1)}
			catalog := diagnosticOutcomeCatalog{outcomeCatalog{CatalogOutcome{State: contract.ActiveCatalogUnavailable, DiagnosticReason: diagnostics.ReasonTimeout, DiagnosticRetryDelay: delay}}}
			manager, err := New(Options{Repository: repository, Driver: driver, Catalog: catalog, Diagnostics: observer})
			require.NoError(t, err)
			t.Cleanup(manager.Shutdown)
			var id string
			for id = range repository.servers {
				break
			}
			manager.Trigger(id, nil, true)
			waitSettlementWorkers(t, manager)
			require.Equal(t, contract.RuntimeActive, manager.Status(id).State)
			operation, err := repository.CreateOperation(t.Context(), servers.OperationRequest{ServerID: id, Kind: contract.OperationRefreshCatalog, ExpectedDesiredRevision: "1"})
			require.NoError(t, err)
			candidate := *manager.entries[id].active
			manager.refreshCatalogOperation(id, candidate.Generation, operation.Operation.ID, candidate, catalog)
			want := diagnostics.DispositionUnknown
			if delay > 0 {
				want = diagnostics.DispositionRetryScheduled
			}
			var warnings int
			for _, fact := range observer.facts {
				if fact.Event == diagnostics.UpstreamUnhealthy {
					warnings++
					require.Equal(t, diagnostics.PhaseToolDiscovery, fact.Phase)
					require.Equal(t, want, fact.Disposition)
				}
			}
			require.Equal(t, 2, warnings)
			joined := catalog.outcome
			joined.DiagnosticJoined, joined.DiagnosticRetryDelay = true, 0
			before := len(observer.facts)
			manager.observeCatalog(candidate, joined)
			require.Len(t, observer.facts, before, "joined results must not emit a stale unknown transition")
		})
	}
}

type diagnosticChallengeCatalog struct{}

func (diagnosticChallengeCatalog) Activate(context.Context, Candidate) CatalogOutcome {
	return CatalogOutcome{State: contract.ActiveCatalogCurrent}
}
func (diagnosticChallengeCatalog) Refresh(context.Context, Candidate) CatalogOutcome {
	return CatalogOutcome{State: contract.ActiveCatalogCurrent, OAuthChallenge: &downstream.OAuthChallengeDisposition{Kind: downstream.OAuthChallengeStepUp, Stage: downstream.OAuthChallengeCatalogFirstPage}}
}
func (diagnosticChallengeCatalog) Withdraw(Candidate, contract.ActiveCatalogState) error { return nil }

func TestExplicitCatalogOAuthOutcomeHasOneObservationOwner(t *testing.T) {
	for _, ref := range []uint64{0, 7} {
		repository := newFakeRepository(1)
		observer := &settlementObserver{}
		catalog := diagnosticChallengeCatalog{}
		manager, err := New(Options{Repository: repository, Catalog: catalog, Diagnostics: observer, DiagnosticReference: func(string) uint64 { return ref }})
		require.NoError(t, err)
		var server servers.Server
		for _, server = range repository.servers {
			break
		}
		current := manager.entryLocked(server.ID)
		candidate := Candidate{Server: server, Generation: current.generation, RuntimeID: "fixture-runtime"}
		current.active = &candidate
		current.status.State, current.status.CatalogState = contract.RuntimeActive, contract.ActiveCatalogCurrent
		// A valid explicit refresh can encounter reconciliation capacity at handoff.
		manager.globalInUse = manager.globalLimit
		operation, err := repository.CreateOperation(t.Context(), servers.OperationRequest{ServerID: server.ID, Kind: contract.OperationRefreshCatalog, ExpectedDesiredRevision: "1"})
		require.NoError(t, err)
		manager.refreshCatalogOperation(server.ID, candidate.Generation, operation.Operation.ID, candidate, catalog)
		require.Len(t, observer.facts, 1, "one catalog result must not inflate suppression or emit duplicate uncorrelated warnings")
		fact := observer.facts[0]
		require.Equal(t, diagnostics.UpstreamUnhealthy, fact.Event, "retained Current catalog plus an auth challenge is not recovery")
		require.Equal(t, ref, fact.Upstream)
		require.Equal(t, diagnostics.DispositionOperatorAuthentication, fact.Disposition)
		settled, err := repository.GetOperation(t.Context(), operation.Operation.ID)
		require.NoError(t, err)
		require.Equal(t, contract.OperationSuperseded, settled.State)
		joined := catalog.Refresh(t.Context(), candidate)
		joined.DiagnosticJoined = true
		require.False(t, manager.HandleCatalogCompletion(candidate, joined, nil))
		require.Len(t, observer.facts, 1, "joined OAuth outcome retains the traversal's single observer")
		<-manager.Drain(t.Context())
	}
}

func TestUpstreamTypedTimeoutDoesNotUseErrorText(t *testing.T) {
	canary := "private-url-token-code-header-payload"
	outcome := constructionFailure(errors.Join(context.DeadlineExceeded, errors.New(canary)))
	require.Equal(t, diagnostics.ReasonTimeout, outcome.DiagnosticReason)
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Warn)
	adapter.Reconciliation(diagnostics.Facts{Event: diagnostics.UpstreamUnhealthy, Phase: diagnostics.PhaseInitialization, Reason: outcome.DiagnosticReason, Disposition: diagnostics.DispositionRetryScheduled})
	require.True(t, adapter.Finish(nil))
	require.Contains(t, output.String(), `"reason":"timeout"`)
	require.NotContains(t, output.String(), canary)
}
