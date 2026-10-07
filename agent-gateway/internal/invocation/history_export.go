package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// ExportHistory uses only the existing bounded history reader. No control
// mutation fence, writer pause, observation queue or filesystem stage is needed.
func (s *ReadService) ExportHistory(ctx context.Context, after int64, limit int) (contract.HistoryExport, error) {
	return s.ExportHistoryThrough(ctx, after, math.MaxInt64, limit)
}

// ExportHistoryThrough bounds a rolling read to an earlier high-water mark. It
// does not pin a snapshot: callers must still compare generation and pruning.
func (s *ReadService) ExportHistoryThrough(ctx context.Context, after, through int64, limit int) (result contract.HistoryExport, err error) {
	if after < 0 || through < after || limit < 1 || limit > contract.HistoryExportMaxRecords {
		return result, ErrInvalidInput
	}
	if s.repository.traffic == nil {
		return result, ErrTrafficFault
	}
	result = contract.HistoryExport{Format: 1, AfterSequence: strconv.FormatInt(after, 10), NextSequence: strconv.FormatInt(after, 10), Absence: contract.HistoryExportAbsence, Records: make([]contract.HistoryExportRecord, 0)}
	err = s.repository.traffic.view(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var high, pruning int64
		if err := tx.QueryRowContext(ctx, `SELECT installation,generation,high_water,pruning,records FROM traffic_meta WHERE singleton=1`).Scan(&result.InstallationID, &result.Generation, &high, &pruning, &result.Retained); err != nil {
			return err
		}
		result.HighWater, result.Pruning = strconv.FormatInt(high, 10), strconv.FormatInt(pruning, 10)
		result.CapturedAt = s.repository.clock.Now().UTC().Format(time.RFC3339Nano)
		// Resolve a bounded cross-protocol sequence window before materializing full
		// records; all reads remain in this same SQLite transaction.
		rows, err := tx.QueryContext(ctx, `SELECT insertion_sequence,protocol FROM (
   SELECT insertion_sequence,'mcp' AS protocol FROM invocations WHERE insertion_sequence>? AND insertion_sequence<=?
   UNION ALL SELECT insertion_sequence,'http' FROM http_traffic WHERE insertion_sequence>? AND insertion_sequence<=?
   UNION ALL SELECT insertion_sequence,'git' FROM git_traffic WHERE insertion_sequence>? AND insertion_sequence<=?
  ) ORDER BY insertion_sequence LIMIT ?`, after, through, after, through, after, through, limit+1)
		if err != nil {
			return err
		}
		type entry struct {
			sequence int64
			protocol string
		}
		entries := make([]entry, 0, limit+1)
		for rows.Next() {
			var item entry
			if err := rows.Scan(&item.sequence, &item.protocol); err != nil {
				return errors.Join(err, rows.Close())
			}
			entries = append(entries, item)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		bytes := 2048 // fixed envelope, IDs, decimal counters and absence statement
		for _, item := range entries {
			if len(result.Records) == limit {
				result.Truncated = true
				break
			}
			record, err := exportRecord(ctx, tx, item.sequence, item.protocol)
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				return err
			}
			if bytes+len(encoded)+1 > contract.HistoryExportMaxBytes {
				result.Truncated = true
				break
			}
			bytes += len(encoded) + 1
			result.Records = append(result.Records, record)
			result.NextSequence = record.Sequence
		}
		return nil
	})
	if err != nil {
		return contract.HistoryExport{}, err
	}
	return result, nil
}

func exportRecord(ctx context.Context, tx *sql.Tx, sequence int64, protocol string) (result contract.HistoryExportRecord, err error) {
	result.Sequence, result.Protocol = strconv.FormatInt(sequence, 10), protocol
	switch protocol {
	case "mcp":
		record, scanErr := scanInvocation(tx.QueryRowContext(ctx, invocationSelect+` WHERE insertion_sequence=?`, sequence))
		if scanErr != nil {
			return result, scanErr
		}
		if !validStoredInvocation(record) {
			return result, ErrInvalidState
		}
		value, projectErr := contract.ProjectInvocationAudit(record)
		if projectErr != nil {
			return result, projectErr
		}
		result.MCP = &value
	case "http":
		value, _, scanErr := scanHTTPTraffic(tx.QueryRowContext(ctx, httpTrafficSelect+` WHERE insertion_sequence=?`, sequence))
		if scanErr != nil {
			return result, scanErr
		}
		result.HTTP = &value
	case "git":
		value, _, scanErr := scanGitTraffic(tx.QueryRowContext(ctx, gitTrafficSelect+` WHERE insertion_sequence=?`, sequence))
		if scanErr != nil {
			return result, scanErr
		}
		result.Git = &value
	default:
		return result, ErrInvalidState
	}
	return result, nil
}
