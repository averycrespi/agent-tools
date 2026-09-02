package servers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

func (repository *Repository) IdempotencyStatus(ctx context.Context) (contract.LimitStatus, error) {
	var inUse int64
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		rows, err := transaction.QueryContext(ctx, `SELECT expires_at FROM s2_idempotency ORDER BY expires_at`)
		if err != nil {
			return fmt.Errorf("read S2 idempotency occupancy: %w", err)
		}
		defer func() { _ = rows.Close() }()
		now := repository.clock.Now()
		for rows.Next() {
			var expiresAt string
			if err := rows.Scan(&expiresAt); err != nil {
				return err
			}
			expires, err := parseTime(expiresAt)
			if err != nil {
				return fmt.Errorf("parse S2 idempotency expiry: %w", err)
			}
			if now.Before(expires) {
				inUse++
			}
		}
		return rows.Err()
	})
	limit := mustLimit("s2_idempotency_records")
	return contract.LimitStatus{InUse: inUse, Limit: limit, Saturated: inUse >= limit}, mapViewError(err)
}

func validateIdempotencyRequest(request *IdempotencyRequest) error {
	if request == nil || !validID(request.AuthorityID) || request.Method == "" || request.Route == "" {
		return fmt.Errorf("%w: idempotency identity", ErrInvalidInput)
	}
	if len(request.Key) < int(contract.IdempotencyKeyMinimumBytes) || int64(len(request.Key)) > mustLimit("idempotency_key_bytes") {
		return fmt.Errorf("%w: idempotency key", ErrInvalidInput)
	}
	for _, character := range []byte(request.Key) {
		if character < 0x21 || character > 0x7e {
			return fmt.Errorf("%w: idempotency key", ErrInvalidInput)
		}
	}
	return nil
}

type idempotencySnapshot struct {
	Server             *Server    `json:"server,omitempty"`
	Operation          *Operation `json:"operation,omitempty"`
	ServerMutationWork *Operation `json:"server_operation,omitempty"`
}

func lookupIdempotencyTx(
	ctx context.Context,
	transaction *sql.Tx,
	request *IdempotencyRequest,
	now time.Time,
) (IdempotencyResult, idempotencySnapshot, bool, error) {
	var result IdempotencyResult
	var snapshot idempotencySnapshot
	var requestHash []byte
	var resultPrecondition string
	var operationID sql.NullString
	var desiredRevision int64
	var resultJSON string
	var expiresAt string
	err := transaction.QueryRowContext(ctx, `
		SELECT request_hash, precondition, result_kind, server_id, operation_id,
		       desired_revision, result_json, expires_at
		FROM s2_idempotency
		WHERE authority_id = ? AND method = ? AND route = ? AND idempotency_key = ?`,
		request.AuthorityID, request.Method, request.Route, request.Key,
	).Scan(
		&requestHash,
		&resultPrecondition,
		&result.Kind,
		&result.ServerID,
		&operationID,
		&desiredRevision,
		&resultJSON,
		&expiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return IdempotencyResult{}, idempotencySnapshot{}, false, nil
	}
	if err != nil {
		return IdempotencyResult{}, idempotencySnapshot{}, false, fmt.Errorf("lookup S2 idempotency: %w", err)
	}
	expires, err := parseTime(expiresAt)
	if err != nil {
		return IdempotencyResult{}, idempotencySnapshot{}, false, fmt.Errorf("parse S2 idempotency expiry: %w", err)
	}
	if !now.Before(expires) {
		return IdempotencyResult{}, idempotencySnapshot{}, false, nil
	}
	if !bytes.Equal(requestHash, request.RequestHash[:]) || resultPrecondition != request.Precondition {
		return IdempotencyResult{}, idempotencySnapshot{}, false, ErrIdempotencyConflict
	}
	result.DesiredRevision = fmt.Sprintf("%d", desiredRevision)
	if operationID.Valid {
		result.OperationID = &operationID.String
	}
	if err := json.Unmarshal([]byte(resultJSON), &snapshot); err != nil {
		return IdempotencyResult{}, idempotencySnapshot{}, false, fmt.Errorf("decode S2 idempotency result: %w", err)
	}
	if err := validateIdempotencySnapshot(result, snapshot); err != nil {
		return IdempotencyResult{}, idempotencySnapshot{}, false, err
	}
	return result, snapshot, true, nil
}

func admitIdempotencyTx(ctx context.Context, transaction *sql.Tx, now time.Time) error {
	rows, err := transaction.QueryContext(ctx, `
		SELECT authority_id, method, route, idempotency_key, expires_at
		FROM s2_idempotency
		ORDER BY expires_at, authority_id, method, route, idempotency_key`)
	if err != nil {
		return fmt.Errorf("inspect S2 idempotency retention: %w", err)
	}
	type expiredRecord struct {
		authorityID string
		method      string
		route       string
		key         string
	}
	expired := make([]expiredRecord, 0)
	for rows.Next() {
		var record expiredRecord
		var expiresAt string
		if err := rows.Scan(&record.authorityID, &record.method, &record.route, &record.key, &expiresAt); err != nil {
			_ = rows.Close()
			return err
		}
		expires, err := parseTime(expiresAt)
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("parse S2 idempotency expiry: %w", err)
		}
		if !now.Before(expires) {
			expired = append(expired, record)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, record := range expired {
		if _, err := transaction.ExecContext(ctx, `
			DELETE FROM s2_idempotency
			WHERE authority_id = ? AND method = ? AND route = ? AND idempotency_key = ?`,
			record.authorityID, record.method, record.route, record.key); err != nil {
			return fmt.Errorf("prune expired S2 idempotency: %w", err)
		}
	}
	var count int64
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM s2_idempotency`).Scan(&count); err != nil {
		return fmt.Errorf("count S2 idempotency records: %w", err)
	}
	if count >= mustLimit("s2_idempotency_records") {
		return ErrResourceLimit
	}
	return nil
}

func insertIdempotencyTx(
	ctx context.Context,
	transaction *sql.Tx,
	request *IdempotencyRequest,
	result IdempotencyResult,
	snapshot idempotencySnapshot,
	now time.Time,
) error {
	if err := validateIdempotencySnapshot(result, snapshot); err != nil {
		return err
	}
	desiredRevision, err := parseRevision(result.DesiredRevision)
	if err != nil || desiredRevision < 1 {
		return ErrInvalidInput
	}
	resultJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode S2 idempotency result: %w", err)
	}
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO s2_idempotency (
			authority_id, method, route, idempotency_key, request_hash, precondition,
			result_kind, server_id, operation_id, desired_revision, result_json,
			created_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		request.AuthorityID,
		request.Method,
		request.Route,
		request.Key,
		request.RequestHash[:],
		request.Precondition,
		result.Kind,
		result.ServerID,
		nullableString(result.OperationID),
		desiredRevision,
		string(resultJSON),
		formatTime(now),
		formatTime(now.Add(contract.IdempotencyRetention)),
	)
	if err != nil {
		return fmt.Errorf("insert S2 idempotency record: %w", err)
	}
	return nil
}

func updateOperationIdempotencySnapshotTx(ctx context.Context, transaction *sql.Tx, operation Operation) error {
	resultJSON, err := json.Marshal(idempotencySnapshot{Operation: &operation})
	if err != nil {
		return fmt.Errorf("encode operation idempotency result: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		UPDATE s2_idempotency SET result_json = ? WHERE operation_id = ?`,
		string(resultJSON), operation.ID); err != nil {
		return fmt.Errorf("update operation idempotency result: %w", err)
	}
	return nil
}

func validateIdempotencySnapshot(result IdempotencyResult, snapshot idempotencySnapshot) error {
	switch result.Kind {
	case "server":
		if snapshot.Server == nil || snapshot.Operation != nil || snapshot.Server.ID != result.ServerID ||
			snapshot.Server.DesiredRevision != result.DesiredRevision || result.OperationID != nil {
			return fmt.Errorf("%w: server idempotency result", ErrInvalidInput)
		}
		if snapshot.ServerMutationWork != nil && (snapshot.ServerMutationWork.ServerID != result.ServerID || snapshot.ServerMutationWork.TargetDesiredRevision != result.DesiredRevision) {
			return fmt.Errorf("%w: server mutation operation result", ErrInvalidInput)
		}
	case "operation":
		if snapshot.Server != nil || snapshot.ServerMutationWork != nil || snapshot.Operation == nil || result.OperationID == nil ||
			snapshot.Operation.ID != *result.OperationID || snapshot.Operation.ServerID != result.ServerID ||
			snapshot.Operation.TargetDesiredRevision != result.DesiredRevision {
			return fmt.Errorf("%w: operation idempotency result", ErrInvalidInput)
		}
	default:
		return ErrInvalidInput
	}
	return nil
}
