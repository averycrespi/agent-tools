package invocation

import (
	"fmt"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
)

type trafficBatchFacts struct {
	observations, admissions, completions, mcp, http, git, expired, unavailable int
	bytes                                                                       int64
}

func (facts trafficBatchFacts) summary(settlement string) string {
	persistence := "not_acknowledged"
	if settlement == "not_started" || settlement == "rolled_back" {
		persistence = "not_persisted"
	}
	if settlement == "committed" {
		persistence = "committed_not_acknowledged"
	}
	return fmt.Sprintf("batch_observations=%d admissions=%d completions=%d mcp=%d http=%d git=%d bytes=%d expired=%d unavailable=%d persistence=%s application_dispatch=unaffected; ", facts.observations, facts.admissions, facts.completions, facts.mcp, facts.http, facts.git, facts.bytes, facts.expired, facts.unavailable, persistence)
}

func (s *TrafficStore) lossLocked(reason diagnostics.TrafficLossReason, terminal bool) {
	stage := 0
	if terminal {
		stage = 1
	}
	if s.lossPending[reason][stage] < contract.RecordedActivityMaxCount {
		s.lossPending[reason][stage]++
	}
}

// Called only after domain/writer gates release. The existing adapter coalesces
// these fixed counters; owners never emit a record for each dropped submission.
func (s *TrafficStore) reportLoss() {
	s.mu.Lock()
	observer, ok := s.diagnostics.(diagnostics.TrafficLossObserver)
	if !ok {
		s.mu.Unlock()
		return
	}
	counts := s.lossPending
	s.lossPending = diagnostics.TrafficLossCounts{}
	s.mu.Unlock()
	for reason, stages := range counts {
		for stage, count := range stages {
			if count != 0 {
				observer.TrafficLoss(diagnostics.TrafficLossReason(reason), stage == 1, count)
			}
		}
	}
}

func trafficPredicate(rule, facts string) error {
	return diagnostics.WithDetail(ErrInvalidState, diagnostics.Detail{Component: "traffic", Operation: "validate", Explanation: "rule=" + rule + " " + facts})
}
