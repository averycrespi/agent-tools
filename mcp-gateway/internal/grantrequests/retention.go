package grantrequests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var errRequestCapacity = errors.New("grant request capacity is exhausted")

func (repository *Repository) ensureRequestCapacity(ctx context.Context, transaction *sql.Tx, principalID string, newEvidenceBytes int64) error {
	rowLimit := repository.capacity.rows
	pendingLimit := repository.capacity.pending
	evidenceLimit := repository.capacity.evidenceBytes
	if newEvidenceBytes < 0 || newEvidenceBytes > fixedLimit("grant_request_evidence_snapshot_bytes") {
		return ErrInvalidState
	}
	var rows, pending, evidence int64
	if err := transaction.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM grant_requests),
		(SELECT count(*) FROM grant_requests WHERE principal_id = ? AND state = 'pending'),
		total_bytes
	FROM grant_request_evidence_bytes WHERE singleton = 1`, principalID).Scan(&rows, &pending, &evidence); err != nil {
		return fmt.Errorf("read grant request capacity: %w", err)
	}
	if rows < 0 || rows > rowLimit || pending < 0 || pending > pendingLimit || evidence < 0 || evidence > evidenceLimit {
		return ErrInvalidState
	}
	if pending >= pendingLimit {
		return errRequestCapacity
	}
	requiredRows := rows + 1 - rowLimit
	if requiredRows < 0 {
		requiredRows = 0
	}
	requiredEvidence := evidence + newEvidenceBytes - evidenceLimit
	if requiredEvidence < 0 {
		requiredEvidence = 0
	}
	if requiredRows == 0 && requiredEvidence == 0 {
		return nil
	}
	victims, freedBytes, lastSequence := int64(0), int64(0), int64(0)
	candidateRows, err := transaction.QueryContext(ctx, `SELECT insertion_sequence,
		COALESCE(length(submitted_evidence), 0) + COALESCE(length(approved_evidence), 0)
	FROM grant_requests WHERE state <> 'pending' ORDER BY insertion_sequence`)
	if err != nil {
		return fmt.Errorf("read terminal request retention candidates: %w", err)
	}
	for candidateRows.Next() {
		var sequence, bytes int64
		if err := candidateRows.Scan(&sequence, &bytes); err != nil {
			_ = candidateRows.Close()
			return fmt.Errorf("read terminal request retention candidate: %w", err)
		}
		if sequence <= lastSequence || bytes < 0 || bytes > 2*fixedLimit("grant_request_evidence_snapshot_bytes") {
			_ = candidateRows.Close()
			return ErrInvalidState
		}
		victims++
		freedBytes += bytes
		lastSequence = sequence
		if victims >= requiredRows && freedBytes >= requiredEvidence {
			break
		}
	}
	if err := candidateRows.Err(); err != nil {
		_ = candidateRows.Close()
		return fmt.Errorf("iterate terminal request retention candidates: %w", err)
	}
	if err := candidateRows.Close(); err != nil {
		return fmt.Errorf("close terminal request retention candidates: %w", err)
	}
	if victims < requiredRows || freedBytes < requiredEvidence {
		return errRequestCapacity
	}
	result, err := transaction.ExecContext(ctx, `DELETE FROM grant_requests
		WHERE state <> 'pending' AND insertion_sequence <= ?`, lastSequence)
	if err != nil {
		return fmt.Errorf("evict terminal grant requests: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil || removed != victims {
		return ErrInvalidState
	}
	return nil
}
