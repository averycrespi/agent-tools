package invocation

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"
)

func (s *TrafficStore) runTraffic() {
	defer close(s.done)
	for {
		// One protected completion batch before each admission batch bounds starvation
		// in both directions. Completion queue bytes are fixed and separately bounded.
		select {
		case r := <-s.terminals:
			s.processTraffic(s.gatherTerminals(r))
		default:
		}
		var first *trafficRequest
		select {
		case first = <-s.admissions:
		default:
			select {
			case first = <-s.admissions:
			case r := <-s.terminals:
				s.processTraffic(s.gatherTerminals(r))
				continue
			case <-s.stop:
				s.drainTraffic()
				return
			}
		}
		batch := []*trafficRequest{first}
		bytes := first.bytes
		timer := time.NewTimer(s.config.Dwell)
		gathering := true
		for gathering && len(batch) < s.config.BatchRecords {
			// Reserve for the largest accepted next member before dequeuing; never split
			// or replay a transaction because an already-dequeued member did not fit.
			if bytes+maxTrafficRecordBytes > s.config.BatchBytes {
				break
			}
			select {
			case r := <-s.admissions:
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

// Drain only already-queued completions: no extra dwell or unbounded preference
// over admissions. Reserve the full bounded diagnostic allowance for each member.
func (s *TrafficStore) gatherTerminals(first *trafficRequest) []*trafficRequest {
	batch := []*trafficRequest{first}
	bytes := first.bytes
	for len(batch) < s.config.BatchRecords && bytes+maxTrafficCompletionBytes <= s.config.BatchBytes {
		select {
		case r := <-s.terminals:
			batch = append(batch, r)
			bytes += r.bytes
		default:
			return batch
		}
	}
	return batch
}

func (s *TrafficStore) drainTraffic() {
	for {
		select {
		case r := <-s.admissions:
			s.settleTraffic(r, nil, ErrTrafficFault)
		case r := <-s.terminals:
			s.settleTraffic(r, nil, ErrTrafficFault)
		default:
			return
		}
	}
}

func (s *TrafficStore) settleTraffic(r *trafficRequest, receipt *TrafficReceipt, err error) {
	s.mu.Lock()
	if r.completion != nil {
		s.terminalQueued--
		delete(s.pins, r.receipt)
	} else {
		s.queued--
		s.queuedBytes -= r.bytes
		s.pendingPins--
		if err == nil && r.ctx.Err() != nil {
			err = errors.Join(ErrTrafficDeadline, r.ctx.Err())
			receipt = nil
		}
		if err == nil {
			s.pins[receipt] = &trafficPin{}
		}
	}
	s.mu.Unlock()
	r.result <- trafficResult{receipt: receipt, err: err}
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
			s.settleTraffic(r, nil, ErrTrafficFault)
		case r.ctx.Err() != nil || !time.Now().Before(r.expires):
			s.settleTraffic(r, nil, errors.Join(ErrTrafficDeadline, r.ctx.Err()))
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
	if err == nil {
		err = s.writeTraffic(ctx, active)
	}
	// Uncertain settlement takes precedence over an otherwise safe refusal.
	// Joined rollback errors retain the original capacity/collision sentinel.
	if err != nil && (errors.Is(err, ErrTrafficFault) ||
		!errors.Is(err, ErrTrafficCapacity) && !errors.Is(err, ErrTrafficDeadline) && !errors.Is(err, ErrIdentityUnavailable)) {
		s.mu.Lock()
		s.faulted = true
		s.mu.Unlock()
		err = errors.Join(ErrTrafficFault, err)
	}
	if errors.Is(err, ErrTrafficCapacity) {
		s.mu.Lock()
		s.quotaRefusals += int64(len(active))
		s.mu.Unlock()
	}
	for _, r := range active {
		var receipt *TrafficReceipt
		if err == nil && r.completion == nil {
			receipt = &TrafficReceipt{evidence: r.prepared, owner: s, request: r.ctx}
		}
		s.settleTraffic(r, receipt, err)
	}
}

func (s *TrafficStore) writeTraffic(ctx context.Context, batch []*trafficRequest) error {
	if ctx.Err() != nil {
		return ErrTrafficDeadline
	}
	if err := s.inject("before_begin"); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	mutationErr := s.applyTraffic(ctx, tx, batch)
	if mutationErr == nil {
		mutationErr = s.inject("statement")
	}
	if mutationErr != nil {
		rollbackErr := tx.Rollback()
		rollbackErr = errors.Join(rollbackErr, s.inject("rollback"))
		if rollbackErr != nil {
			return errors.Join(ErrTrafficFault, mutationErr, rollbackErr)
		}
		return mutationErr
	}
	if err = s.inject("commit"); err != nil {
		return errors.Join(ErrTrafficFault, err, tx.Rollback(), s.inject("rollback"))
	}
	// database/sql may roll back internally after a failed driver commit; that
	// cannot establish success or prove its rollback. Every commit error faults.
	if err = tx.Commit(); err != nil {
		return errors.Join(ErrTrafficFault, err)
	}
	if err = s.inject("acknowledgment"); err != nil {
		return errors.Join(ErrTrafficFault, err)
	}
	return trafficFiles(s.path, s.config)
}

func (s *TrafficStore) applyTraffic(ctx context.Context, tx *sql.Tx, batch []*trafficRequest) error {
	if batch[0].completion != nil {
		for _, r := range batch {
			result, err := tx.ExecContext(ctx, `UPDATE invocations SET completed_at=?,terminal_class=?,failure_diagnostics=? WHERE id=? AND completed_at IS NULL AND terminal_class IS NULL`, r.completion.CompletedAt, string(r.completion.Class), r.diagnosticJSON, r.receipt.evidence.InvocationID)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if changed != 1 {
				return ErrInvalidState
			}
		}
		return nil
	}
	ids := make(map[string]bool, len(batch))
	var incoming int64
	// Check every collision before even selecting pruning victims.
	for _, r := range batch {
		id := r.prepared.InvocationID
		if ids[id] {
			return ErrIdentityUnavailable
		}
		ids[id] = true
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM invocations WHERE id=?)`, id).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return ErrIdentityUnavailable
		}
		incoming += r.bytes
	}
	var count, bytes, high int64
	if err := tx.QueryRowContext(ctx, `SELECT records,bytes,high_water FROM traffic_meta WHERE singleton=1`).Scan(&count, &bytes, &high); err != nil {
		return err
	}
	if high > math.MaxInt64-int64(len(batch)) {
		return ErrTrafficCapacity
	}
	pinned := map[string]bool{}
	s.mu.Lock()
	for receipt := range s.pins {
		pinned[receipt.evidence.InvocationID] = true
	}
	s.mu.Unlock()
	// Logical charged retention leaves most database pages for indexes, fragmentation
	// and SQLite overhead. Pruning work is independently bounded per transaction.
	victims := make([]string, 0)
	if count+int64(len(batch)) > s.config.RetainedRecords || bytes+incoming > s.retentionBytes() {
		rows, err := tx.QueryContext(ctx, `SELECT invocations.id,traffic_sizes.bytes FROM invocations JOIN traffic_sizes USING(id) ORDER BY insertion_sequence LIMIT ?`, s.config.ActiveRecords+s.config.BatchRecords+256)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var charge int64
			if err = rows.Scan(&id, &charge); err != nil {
				break
			}
			if pinned[id] {
				continue
			}
			victims = append(victims, id)
			count--
			bytes -= charge
			if count+int64(len(batch)) <= s.config.RetainedRecords && bytes+incoming <= s.retentionBytes() {
				break
			}
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
	}
	if count+int64(len(batch)) > s.config.RetainedRecords || bytes+incoming > s.retentionBytes() {
		return ErrTrafficCapacity
	}
	for _, id := range victims {
		if _, err := tx.ExecContext(ctx, `DELETE FROM invocations WHERE id=?`, id); err != nil {
			return err
		}
	}
	for _, r := range batch {
		values, err := admissionSQLValues(r.prepared)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO invocations (
 id,principal_id,credential_id,credential_fingerprint,credential_revision,
 admitted_at,admission_class,requested_name,redacted_arguments,server_id,tool_id,upstream_name,
 descriptor_revision,descriptor_fingerprint,decision,authorization_revision,evaluated_at,grant_id
 ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, values...); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO traffic_sizes VALUES(?,?)`, r.prepared.InvocationID, r.bytes); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE traffic_meta SET high_water=?,pruning=pruning+? WHERE singleton=1`, high+int64(len(batch)), len(victims))
	return err
}
