package invocation

import (
	"context"
	"database/sql"
	"os"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

func (s *TrafficStore) Status(ctx context.Context) contract.TrafficStatus {
	s.mu.Lock()
	status := contract.TrafficStatus{Ready: !s.closed && !s.faulted && !s.draining, Faulted: s.faulted, Pressure: s.queued >= s.config.QueueRecords || len(s.pins)+s.pendingPins >= s.config.ActiveRecords, BudgetBytes: s.config.BudgetBytes, QuotaRefusals: s.quotaRefusals, RollingHistory: true, UnknownCompletionPossible: true}
	s.mu.Unlock()
	if info, err := os.Stat(s.path); err == nil {
		status.DatabaseBytes = info.Size()
	}
	if info, err := os.Stat(s.path + "-wal"); err == nil {
		status.WALBytes = info.Size()
	}
	if status.DatabaseBytes+status.WALBytes+trafficReservation(s.config) > status.BudgetBytes {
		status.Pressure = true
	}
	err := s.view(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT generation,pruning FROM traffic_meta WHERE singleton=1`).Scan(&status.Generation, &status.PrunedRecords)
	})
	if err != nil {
		status.Ready = false
		status.Pressure = true
	}
	return status
}
