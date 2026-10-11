package invocation

import (
	"context"
	"database/sql"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// These are request outcomes, not admission or transport-success counters.
// Git mirrors contract.GitTrafficOutcome; a push needs its actual upstream report.
const protocolActivitySQL = `WITH classified(protocol, admitted, outcome) AS (
 SELECT 'mcp', admitted_at, CASE
 WHEN admission_class != 'evaluated' THEN 'rejected'
 WHEN decision IN ('deny','block') THEN 'denied'
 WHEN terminal_class = 'succeeded' THEN 'success'
 WHEN terminal_class IN ('prestart_failure','downstream_failure') THEN 'failed'
 ELSE 'unknown' END FROM invocations
 UNION ALL
 SELECT 'http', json_extract(admission,'$.admitted_at'), CASE
 WHEN decision = 'block' THEN 'denied'
 WHEN outcome IN ('prestart_failure','upstream_failure') OR json_extract(completion,'$.status') >= 400 THEN 'failed'
 WHEN outcome = 'succeeded' AND json_extract(completion,'$.status') BETWEEN 200 AND 299 THEN 'success'
 WHEN outcome = 'succeeded' AND json_extract(completion,'$.status') BETWEEN 100 AND 399 THEN 'other'
 ELSE 'unknown' END FROM http_traffic WHERE traffic_type = 'request'
 UNION ALL
 SELECT 'git', json_extract(admission,'$.admitted_at'), CASE
 WHEN json_extract(admission,'$.allowed') = 0 THEN 'denied'
 WHEN completion IS NULL THEN 'unknown'
 WHEN json_extract(completion,'$.outcome') = 'prestart_failure' OR json_extract(completion,'$.status') >= 400 THEN 'failed'
 WHEN json_extract(completion,'$.transfer_complete') = 0 THEN 'incomplete'
 WHEN json_extract(admission,'$.operation') = 'push' THEN CASE json_extract(completion,'$.reported_result')
  WHEN 'reported_success' THEN 'reported_success'
  WHEN 'reported_failure' THEN 'failed'
  WHEN 'reported_partial' THEN 'reported_partial'
  ELSE 'unknown' END
 WHEN json_extract(completion,'$.status') BETWEEN 200 AND 299 THEN 'success'
 ELSE 'unknown' END FROM git_traffic
) SELECT protocol, outcome, count(*) FROM classified WHERE admitted >= ? AND admitted < ? GROUP BY protocol, outcome`

func (s *ReadService) ProtocolActivity(ctx context.Context, window string) (contract.ProtocolActivity, error) {
	duration := contract.ProtocolActivityWindow(window)
	if duration == 0 {
		return contract.ProtocolActivity{}, ErrInvalidInput
	}
	now := s.repository.clock.Now()
	result := contract.ProtocolActivity{Window: window, From: canonicalInvocationTime(now.Add(-duration)), Until: canonicalInvocationTime(now), Coverage: "unavailable"}
	if !contract.ValidHistoryRange(result.From, result.Until) {
		return contract.ProtocolActivity{}, ErrInvalidState
	}
	if s.repository.traffic == nil {
		return result, nil
	}
	return s.repository.traffic.protocolActivity(ctx, result), nil
}
func (s *TrafficStore) protocolActivity(ctx context.Context, result contract.ProtocolActivity) contract.ProtocolActivity {
	if s.optional != nil {
		target := s.optionalTarget()
		if target == nil {
			return result
		}
		return target.protocolActivity(ctx, result)
	}
	// Optional aggregation can scan a large retained population. Give it a caller
	// deadline shorter than the store's fault-detecting read lifetime: expensive
	// summaries become unavailable, never a recording fault or truncated total.
	ctx, cancel := context.WithTimeout(ctx, s.config.ReadLifetime/2)
	defer cancel()
	counts := &contract.ProtocolActivityCounts{}
	var pruning int64
	err := s.view(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT pruning FROM traffic_meta WHERE singleton=1`).Scan(&pruning); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, protocolActivitySQL, result.From, result.Until)
		if err != nil {
			return err
		}
		for rows.Next() {
			var protocol, outcome string
			var count int64
			if err = rows.Scan(&protocol, &outcome, &count); err != nil {
				break
			}
			var target *contract.ProtocolOutcomes
			switch protocol {
			case "http":
				target = &counts.HTTP
			case "git":
				target = &counts.Git
			case "mcp":
				target = &counts.MCP
			default:
				err = ErrInvalidState
			}
			if err != nil {
				break
			}
			if err = addProtocolOutcome(target, outcome, count); err != nil {
				break
			}
		}
		return errors.Join(err, rows.Err(), rows.Close())
	})
	if err != nil {
		return result
	}
	result.Counts = counts
	result.Coverage = "retained"
	s.mu.Lock()
	partial := pruning != 0 || s.quotaRefusals != 0 || s.queued != 0 || s.faulted || s.draining || s.closed
	s.mu.Unlock()
	if partial {
		result.Coverage = "partial"
	}
	return result
}
func addProtocolOutcome(counts *contract.ProtocolOutcomes, outcome string, count int64) error {
	if count < 0 || count > int64(contract.RecordedActivityMaxCount)-counts.Total {
		return ErrInvalidState
	}
	counts.Total += count
	switch outcome {
	case "success":
		counts.Success += count
	case "other":
		counts.Other += count
	case "reported_success":
		counts.ReportedSuccess += count
	case "failed":
		counts.Failed += count
	case "denied":
		counts.Denied += count
	case "rejected":
		counts.Rejected += count
	case "unknown":
		counts.Unknown += count
	case "incomplete":
		counts.Incomplete += count
	case "reported_partial":
		counts.ReportedPartial += count
	default:
		return ErrInvalidState
	}
	return nil
}
