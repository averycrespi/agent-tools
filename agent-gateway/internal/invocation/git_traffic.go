package invocation

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/activity"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

func (s *TrafficStore) AdmitGit(ctx context.Context, a contract.GitTrafficAdmission) (*TrafficReceipt, error) {
	encoded, err := encodeGitAdmission(a)
	if err != nil {
		return nil, err
	}
	return s.enqueueAdmission(&trafficRequest{ctx: ctx, prepared: PreparedAdmission{Identity: activity.Identity{InvocationID: a.ID, AdmittedAt: a.AdmittedAt}}, gitAdmission: encoded, gitAllowed: a.Allowed, bytes: gitTrafficChargeBase + int64(len(encoded)), expires: time.Now().Add(s.config.QueueLifetime), result: make(chan trafficResult, 1)})
}
func (s *TrafficStore) CompleteGit(ctx context.Context, receipt *TrafficReceipt, c contract.GitTrafficCompletion) error {
	var a contract.GitTrafficAdmission
	valid := receipt != nil && receipt.gitAdmission != "" && receipt.httpAdmission == ""
	if valid {
		valid = strictjson.Decode([]byte(receipt.gitAdmission), &a, strictjson.Options{MaxBytes: contract.GitTrafficAdmissionBytes, MaxDepth: 4, RejectUnknownMembers: true}) == nil
	}
	encoded, err := encodeGitCompletion(a, c)
	return s.enqueueCompletion(&trafficRequest{ctx: ctx, receipt: receipt, gitCompletion: encoded, bytes: maxTrafficCompletionBytes, expires: time.Now().Add(s.config.QueueLifetime), result: make(chan trafficResult, 1)}, valid && err == nil)
}

// GitHistory is an internal bounded evidence seam for validation, not a public
// operator API or a reconstruction of dispatch authority.
type GitTrafficHistory struct {
	Generation         string
	HighWater, Pruning int64
	Records            []contract.GitTrafficRecord
}

func (s *TrafficStore) GitHistory(ctx context.Context, after int64, limit int) (out GitTrafficHistory, err error) {
	if after < 0 || limit < 1 || limit > 256 {
		return out, ErrInvalidInput
	}
	err = s.view(ctx, func(tx *sql.Tx) error {
		if e := tx.QueryRowContext(ctx, `SELECT generation,high_water,pruning FROM traffic_meta WHERE singleton=1`).Scan(&out.Generation, &out.HighWater, &out.Pruning); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, gitTrafficSelect+` WHERE insertion_sequence>? ORDER BY insertion_sequence LIMIT ?`, after, limit)
		if e != nil {
			return e
		}
		for rows.Next() {
			record, _, scanErr := scanGitTraffic(rows)
			if scanErr != nil {
				e = scanErr
				break
			}
			out.Records = append(out.Records, record)
		}
		return errors.Join(e, rows.Err(), rows.Close())
	})
	if err != nil {
		return GitTrafficHistory{}, err
	}
	return
}
func (s *TrafficStore) validateGitTraffic(ctx context.Context, high int64) (count, charged int64, result error) {
	rows, err := s.db.QueryContext(ctx, gitTrafficSelect+` ORDER BY insertion_sequence LIMIT ?`, s.config.RetainedRecords+1)
	if err != nil {
		return 0, 0, err
	}
	var previous int64
	for rows.Next() {
		record, charge, err := scanGitTraffic(rows)
		if err != nil {
			result = err
			break
		}
		if count >= s.config.RetainedRecords || record.Sequence <= previous || record.Sequence > high {
			result = ErrInvalidState
			break
		}
		previous = record.Sequence
		count++
		charged += charge
	}
	result = errors.Join(result, rows.Err(), rows.Close())
	if result != nil {
		return 0, 0, result
	}
	var collision bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM git_traffic g JOIN (SELECT id,insertion_sequence FROM invocations UNION ALL SELECT id,insertion_sequence FROM http_traffic) p ON p.id=g.id OR p.insertion_sequence=g.insertion_sequence)`).Scan(&collision); err != nil {
		return 0, 0, err
	}
	if collision {
		return 0, 0, ErrInvalidState
	}
	return count, charged, nil
}
