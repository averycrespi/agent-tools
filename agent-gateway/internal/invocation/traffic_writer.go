package invocation

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

func (s *TrafficStore) runTraffic() {
	defer close(s.done)
	for {
		var first *trafficRequest
		select {
		case first = <-s.observations:
		case <-s.stop:
			s.drainTraffic()
			return
		}
		batch := []*trafficRequest{first}
		bytes := first.bytes
		timer := time.NewTimer(s.config.Dwell)
		gathering := true
		for gathering && len(batch) < s.config.BatchRecords && bytes+maxTrafficRecordBytes+maxTrafficCompletionBytes <= s.config.BatchBytes {
			select {
			case r := <-s.observations:
				batch = append(batch, r)
				bytes += r.bytes
			case <-timer.C:
				gathering = false
			case <-s.stop:
				gathering = false
			}
		}
		timer.Stop()
		s.processTraffic(batch)
	}
}
func (s *TrafficStore) drainTraffic() {
	for {
		select {
		case r := <-s.observations:
			s.settleTraffic(r, ErrTrafficFault)
		default:
			return
		}
	}
}
func (s *TrafficStore) settleTraffic(r *trafficRequest, err error) {
	s.mu.Lock()
	s.queued--
	s.queuedBytes -= r.bytes
	if r.terminal() {
		s.completionQueued--
	}
	if err != nil {
		s.dropLocked()
	} else if s.acknowledged < contract.RecordedActivityMaxCount {
		s.acknowledged++
	}
	s.mu.Unlock()
}
func (s *TrafficStore) processTraffic(batch []*trafficRequest) {
	s.writerGate.Lock()
	defer s.writerGate.Unlock()
	active := make([]*trafficRequest, 0, len(batch))
	for _, r := range batch {
		s.mu.Lock()
		unavailable := s.closed || s.faulted
		s.mu.Unlock()
		switch {
		case unavailable:
			s.settleTraffic(r, ErrTrafficFault)
		case !time.Now().Before(r.expires):
			s.settleTraffic(r, ErrTrafficDeadline)
		default:
			active = append(active, r)
		}
	}
	if len(active) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.config.WriteLifetime)
	defer cancel()
	err := s.reserveTraffic(ctx)
	var recorded []recordedEvent
	if err == nil {
		recorded, err = s.writeTraffic(ctx, active)
	}
	if err != nil && (errors.Is(err, ErrTrafficFault) ||
		!errors.Is(err, ErrTrafficCapacity) && !errors.Is(err, ErrTrafficDeadline) && !errors.Is(err, ErrIdentityUnavailable)) {
		s.mu.Lock()
		s.faulted = true
		s.mu.Unlock()
		err = errors.Join(ErrTrafficFault, err)
	}
	if err == nil {
		for _, event := range recorded {
			s.recorded.record(event)
		}
	}
	for _, r := range active {
		s.settleTraffic(r, err)
	}
	s.mu.Lock()
	invalidate := s.invalidate
	s.mu.Unlock()
	if invalidate != nil {
		invalidate(contract.Invalidation{Kind: contract.InvalidationSystemStatus})
		if err == nil {
			invalidate(contract.Invalidation{Kind: contract.InvalidationInvocations})
		}
	}
}
func (s *TrafficStore) writeTraffic(ctx context.Context, batch []*trafficRequest) ([]recordedEvent, error) {
	if ctx.Err() != nil {
		return nil, ErrTrafficDeadline
	}
	if err := s.inject("before_begin"); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	recorded, mutationErr := s.applyTraffic(ctx, tx, batch)
	if mutationErr == nil {
		mutationErr = s.inject("statement")
	}
	if mutationErr != nil {
		rollbackErr := errors.Join(tx.Rollback(), s.inject("rollback"))
		if rollbackErr != nil {
			return nil, errors.Join(ErrTrafficFault, mutationErr, rollbackErr)
		}
		return nil, mutationErr
	}
	if err = s.inject("commit"); err != nil {
		return nil, errors.Join(ErrTrafficFault, err, tx.Rollback(), s.inject("rollback"))
	}
	if err = tx.Commit(); err != nil {
		return nil, errors.Join(ErrTrafficFault, err)
	}
	if err = s.inject("acknowledgment"); err != nil {
		return nil, errors.Join(ErrTrafficFault, err)
	}
	if err = trafficFiles(s.path, s.config); err != nil {
		return nil, err
	}
	return recorded, nil
}

func (s *TrafficStore) applyTraffic(ctx context.Context, tx *sql.Tx, batch []*trafficRequest) ([]recordedEvent, error) {
	// Validate all identities before any pruning. Matching snapshots may update
	// their first terminal only; a late initial observation cannot regress it.
	for _, r := range batch {
		if _, err := matchingTraffic(ctx, tx, r.observation); err != nil {
			return nil, err
		}
	}
	recorded := make([]recordedEvent, 0, len(batch)*2)
	for _, r := range batch {
		exists, err := matchingTraffic(ctx, tx, r.observation)
		if err != nil {
			return nil, err
		}
		if !exists {
			if err = s.insertTraffic(ctx, tx, r.observation); err != nil {
				return nil, err
			}
			if r.observation.gitAdmission == "" {
				recorded = append(recorded, r.observation.recorded)
			}
		}
		if !r.terminal() {
			continue
		}
		var result sql.Result
		id := r.observation.prepared.InvocationID
		switch {
		case r.gitCompletion != "":
			result, err = tx.ExecContext(ctx, `UPDATE git_traffic SET completion=? WHERE id=? AND completion IS NULL`, r.gitCompletion, id)
		case r.httpCompletion != "":
			result, err = tx.ExecContext(ctx, `UPDATE http_traffic SET completion=? WHERE id=? AND completion IS NULL`, r.httpCompletion, id)
		default:
			result, err = tx.ExecContext(ctx, `UPDATE invocations SET completed_at=?,terminal_class=?,failure_diagnostics=? WHERE id=? AND completed_at IS NULL AND terminal_class IS NULL`, r.completion.CompletedAt, string(r.completion.Class), r.diagnosticJSON, id)
		}
		if err != nil {
			return nil, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if changed == 1 && r.observation.gitAdmission == "" {
			recorded = append(recorded, r.recorded)
		}
	}
	return recorded, nil
}

// A correlation collision never changes another observation's immutable facts.
func matchingTraffic(ctx context.Context, tx *sql.Tx, o TrafficObservation) (bool, error) {
	id := o.prepared.InvocationID
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM invocations WHERE id=? UNION ALL SELECT 1 FROM http_traffic WHERE id=? UNION ALL SELECT 1 FROM git_traffic WHERE id=?)`, id, id, id).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	var matches bool
	var err error
	switch {
	case o.gitAdmission != "":
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM git_traffic WHERE id=? AND admission=?)`, id, o.gitAdmission).Scan(&matches)
	case o.httpAdmission != "":
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM http_traffic WHERE id=? AND admission=?)`, id, o.httpAdmission).Scan(&matches)
	default:
		values, e := admissionSQLValues(o.prepared)
		if e != nil {
			return false, e
		}
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM invocations WHERE id IS ? AND principal_id IS ? AND credential_id IS ? AND credential_fingerprint IS ? AND credential_revision IS ? AND admitted_at IS ? AND admission_class IS ? AND requested_name IS ? AND redacted_arguments IS ? AND server_id IS ? AND tool_id IS ? AND upstream_name IS ? AND descriptor_revision IS ? AND descriptor_fingerprint IS ? AND decision IS ? AND authorization_revision IS ? AND evaluated_at IS ? AND grant_id IS ?)`, values...).Scan(&matches)
	}
	if err != nil {
		return false, err
	}
	if !matches {
		return false, ErrIdentityUnavailable
	}
	return true, nil
}

func (s *TrafficStore) insertTraffic(ctx context.Context, tx *sql.Tx, o TrafficObservation) error {
	var count, bytes, high int64
	if err := tx.QueryRowContext(ctx, `SELECT records,bytes,high_water FROM traffic_meta WHERE singleton=1`).Scan(&count, &bytes, &high); err != nil {
		return err
	}
	if high == math.MaxInt64 {
		return ErrTrafficCapacity
	}
	victims := make([]string, 0)
	if count+1 > s.config.RetainedRecords || bytes+o.bytes > s.retentionBytes() {
		rows, err := tx.QueryContext(ctx, `SELECT id,bytes FROM (SELECT invocations.id,traffic_sizes.bytes,insertion_sequence FROM invocations JOIN traffic_sizes USING(id) UNION ALL SELECT id,bytes,insertion_sequence FROM http_traffic UNION ALL SELECT id,bytes,insertion_sequence FROM git_traffic) ORDER BY insertion_sequence LIMIT ?`, s.config.BatchRecords+256)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var charge int64
			if err = rows.Scan(&id, &charge); err != nil {
				break
			}
			victims = append(victims, id)
			count--
			bytes -= charge
			if count+1 <= s.config.RetainedRecords && bytes+o.bytes <= s.retentionBytes() {
				break
			}
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
	}
	if count+1 > s.config.RetainedRecords || bytes+o.bytes > s.retentionBytes() {
		return ErrTrafficCapacity
	}
	for _, id := range victims {
		if _, err := tx.ExecContext(ctx, `DELETE FROM invocations WHERE id=?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM http_traffic WHERE id=?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM git_traffic WHERE id=?`, id); err != nil {
			return err
		}
	}
	sequence := high + 1
	switch {
	case o.gitAdmission != "":
		if _, err := tx.ExecContext(ctx, `INSERT INTO git_traffic(insertion_sequence,id,admission,bytes) VALUES(?,?,?,?)`, sequence, o.prepared.InvocationID, o.gitAdmission, o.bytes); err != nil {
			return err
		}
	case o.httpAdmission != "":
		if _, err := tx.ExecContext(ctx, `INSERT INTO http_traffic(insertion_sequence,id,admission,bytes) VALUES(?,?,?,?)`, sequence, o.prepared.InvocationID, o.httpAdmission, o.bytes); err != nil {
			return err
		}
	default:
		values, err := admissionSQLValues(o.prepared)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO invocations (insertion_sequence,id,principal_id,credential_id,credential_fingerprint,credential_revision,admitted_at,admission_class,requested_name,redacted_arguments,server_id,tool_id,upstream_name,descriptor_revision,descriptor_fingerprint,decision,authorization_revision,evaluated_at,grant_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, append([]any{sequence}, values...)...); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO traffic_sizes VALUES(?,?)`, o.prepared.InvocationID, o.bytes); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sqlite_sequence WHERE name='invocations'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sqlite_sequence(name,seq) VALUES('invocations',?)`, sequence); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE traffic_meta SET high_water=?,pruning=pruning+? WHERE singleton=1`, sequence, len(victims))
	return err
}
