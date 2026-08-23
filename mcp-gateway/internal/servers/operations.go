package servers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

const operationCollection = "server_operations"

func (repository *Repository) CreateOperation(ctx context.Context, request OperationRequest) (OperationResult, error) {
	if !validID(request.ServerID) {
		return OperationResult{}, ErrNotFound
	}
	if _, err := contract.ParseServerOperationKind(string(request.Kind)); err != nil {
		return OperationResult{}, ErrInvalidInput
	}
	if request.Idempotency != nil {
		if err := validateIdempotencyRequest(request.Idempotency); err != nil {
			return OperationResult{}, err
		}
	}
	operationID := request.ID
	var err error
	if operationID == "" {
		operationID, err = repository.NewID()
		if err != nil {
			return OperationResult{}, err
		}
	}
	if !validID(operationID) {
		return OperationResult{}, ErrInvalidInput
	}

	var result OperationResult
	err = repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		now := repository.clock.Now()
		if request.Idempotency != nil {
			stored, snapshot, found, lookupErr := lookupIdempotencyTx(ctx, transaction, request.Idempotency, now)
			if lookupErr != nil {
				return lookupErr
			}
			if found {
				result = OperationResult{Operation: *snapshot.Operation, Result: stored, Replayed: true}
				return nil
			}
			if err := admitIdempotencyTx(ctx, transaction, now); err != nil {
				return err
			}
		}

		server, getErr := serverByIDTx(ctx, transaction, request.ServerID)
		if getErr != nil {
			return getErr
		}
		if request.ExpectedDesiredRevision != "" && request.ExpectedDesiredRevision != server.DesiredRevision {
			return ErrStaleRevision
		}
		authority, err := authorityTx(ctx, transaction, request.ServerID)
		if err != nil {
			return err
		}
		desiredRevision, err := strconv.ParseInt(server.DesiredRevision, 10, 64)
		if err != nil {
			return fmt.Errorf("parse operation target desired revision: %w", err)
		}
		staticRevision, err := strconv.ParseInt(authority.CredentialRevisions.StaticCredential, 10, 64)
		if err != nil {
			return err
		}
		oauthClientRevision, err := strconv.ParseInt(authority.CredentialRevisions.OAuthClient, 10, 64)
		if err != nil {
			return err
		}
		oauthTokensRevision, err := strconv.ParseInt(authority.CredentialRevisions.OAuthTokens, 10, 64)
		if err != nil {
			return err
		}
		createdAt := formatTime(now)
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO server_operations (
				id, server_id, kind, target_desired_revision,
				target_static_credential_revision, target_oauth_client_revision,
				target_oauth_tokens_revision, state, reason, created_at, started_at, finished_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, 'scheduled', NULL, ?, NULL, NULL)`,
			operationID,
			request.ServerID,
			request.Kind,
			desiredRevision,
			staticRevision,
			oauthClientRevision,
			oauthTokensRevision,
			createdAt,
		); err != nil {
			return fmt.Errorf("insert server operation: %w", err)
		}
		operation, err := operationByIDTx(ctx, transaction, operationID)
		if err != nil {
			return err
		}
		stored := IdempotencyResult{
			Kind: "operation", ServerID: request.ServerID, OperationID: &operationID,
			DesiredRevision: server.DesiredRevision,
		}
		if request.Idempotency != nil {
			if err := insertIdempotencyTx(ctx, transaction, request.Idempotency, stored, idempotencySnapshot{Operation: &operation}, now); err != nil {
				return err
			}
		}
		result = OperationResult{Operation: operation, Result: stored}
		return nil
	})
	return result, mapMutationError(err)
}

func (repository *Repository) GetOperation(ctx context.Context, operationID string) (Operation, error) {
	if !validID(operationID) {
		return Operation{}, ErrNotFound
	}
	var operation Operation
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		var err error
		operation, err = operationByIDTx(ctx, transaction, operationID)
		return err
	})
	return operation, mapViewError(err)
}

func (repository *Repository) TransitionOperation(
	ctx context.Context,
	operationID string,
	state contract.ServerOperationState,
	reason *contract.PublicReason,
) (Operation, error) {
	if !validID(operationID) {
		return Operation{}, ErrNotFound
	}
	if _, err := contract.ParseServerOperationState(string(state)); err != nil {
		return Operation{}, ErrInvalidTransition
	}
	if reason != nil {
		if _, err := contract.ParsePublicReason(string(*reason)); err != nil {
			return Operation{}, ErrInvalidInput
		}
	}
	var updated Operation
	err := repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		current, err := operationByIDTx(ctx, transaction, operationID)
		if err != nil {
			return err
		}
		if !operationTransitionAllowed(current.State, state) {
			return ErrInvalidTransition
		}
		startedAt := current.StartedAt
		if state == contract.OperationRunning && startedAt == nil {
			value := formatTime(repository.clock.Now())
			startedAt = &value
		}
		finishedAt := current.FinishedAt
		if operationTerminal(state) {
			value := formatTime(repository.clock.Now())
			finishedAt = &value
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE server_operations
			SET state = ?, reason = ?, started_at = ?, finished_at = ?
			WHERE id = ?`, state, nullableReason(reason), nullableString(startedAt), nullableString(finishedAt), operationID); err != nil {
			return fmt.Errorf("transition server operation: %w", err)
		}
		updated, err = operationByIDTx(ctx, transaction, operationID)
		if err != nil {
			return err
		}
		if err := updateOperationIdempotencySnapshotTx(ctx, transaction, updated); err != nil {
			return err
		}
		if operationTerminal(state) {
			return pruneTerminalOperationsTx(ctx, transaction, current.ServerID)
		}
		return nil
	})
	return updated, mapMutationError(err)
}

func (repository *Repository) InterruptNonterminal(ctx context.Context) error {
	return mapMutationError(repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		now := formatTime(repository.clock.Now())
		rows, err := transaction.QueryContext(ctx, `
			SELECT id, server_id FROM server_operations
			WHERE state IN ('scheduled', 'running') ORDER BY server_id, insertion_sequence, id`)
		if err != nil {
			return fmt.Errorf("list nonterminal operations: %w", err)
		}
		operationIDs := make([]string, 0)
		serverIDs := make([]string, 0)
		lastServerID := ""
		for rows.Next() {
			var operationID, serverID string
			if err := rows.Scan(&operationID, &serverID); err != nil {
				_ = rows.Close()
				return err
			}
			operationIDs = append(operationIDs, operationID)
			if serverID != lastServerID {
				serverIDs = append(serverIDs, serverID)
				lastServerID = serverID
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE server_operations
			SET state = 'interrupted', reason = 'interrupted', finished_at = ?
			WHERE state IN ('scheduled', 'running')`, now); err != nil {
			return fmt.Errorf("interrupt nonterminal operations: %w", err)
		}
		for _, operationID := range operationIDs {
			operation, err := operationByIDTx(ctx, transaction, operationID)
			if err != nil {
				return err
			}
			if err := updateOperationIdempotencySnapshotTx(ctx, transaction, operation); err != nil {
				return err
			}
		}
		for _, serverID := range serverIDs {
			if err := pruneTerminalOperationsTx(ctx, transaction, serverID); err != nil {
				return err
			}
		}
		return nil
	}))
}

func (repository *Repository) ListOperations(
	ctx context.Context,
	serverID string,
	cursor *SnapshotCursor,
	limit int,
) (OperationPage, error) {
	if !validID(serverID) {
		return OperationPage{}, ErrNotFound
	}
	if err := validatePageLimit(limit); err != nil {
		return OperationPage{}, err
	}
	var page OperationPage
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		if err := ensureServerExists(ctx, transaction, serverID); err != nil {
			return err
		}
		var pruningGeneration int64
		if err := transaction.QueryRowContext(ctx, `
			SELECT pruning_generation FROM server_operation_watermarks WHERE server_id = ?`, serverID).Scan(&pruningGeneration); err != nil {
			return fmt.Errorf("read operation pruning generation: %w", err)
		}
		snapshot := SnapshotCursor{Collection: operationCollection, ServerID: serverID, Floor: pruningGeneration}
		if cursor == nil {
			if err := transaction.QueryRowContext(ctx, `
				SELECT coalesce(max(insertion_sequence), 0)
				FROM server_operations WHERE server_id = ?`, serverID).Scan(&snapshot.Upper); err != nil {
				return fmt.Errorf("capture operation insertion watermark: %w", err)
			}
		} else {
			snapshot = *cursor
			if snapshot.Collection != operationCollection || snapshot.ServerID != serverID || snapshot.Floor < 0 || snapshot.After < 0 || snapshot.After > snapshot.Upper {
				return ErrStaleCursor
			}
			if pruningGeneration != snapshot.Floor {
				return ErrStaleCursor
			}
		}
		rows, err := transaction.QueryContext(ctx, operationSelect+`
			WHERE server_id = ? AND insertion_sequence > ? AND insertion_sequence <= ?
			ORDER BY insertion_sequence, id LIMIT ?`, serverID, snapshot.After, snapshot.Upper, limit+1)
		if err != nil {
			return fmt.Errorf("list server operations: %w", err)
		}
		defer func() { _ = rows.Close() }()
		items := make([]Operation, 0, limit+1)
		for rows.Next() {
			operation, scanErr := scanOperation(rows)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, operation)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(items) > limit {
			last := items[limit-1]
			next := snapshot
			next.After = last.InsertionSequence
			next.AfterID = last.ID
			page.Next = &next
			items = items[:limit]
		}
		page.Items = items
		return nil
	})
	return page, mapViewError(err)
}

func (repository *Repository) OperationStatus(ctx context.Context, serverID string) (contract.LimitStatus, error) {
	var inUse int64
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		return transaction.QueryRowContext(ctx, `
			SELECT count(*) FROM server_operations
			WHERE server_id = ? AND state NOT IN ('scheduled', 'running')`, serverID).Scan(&inUse)
	})
	limit := mustLimit("terminal_operations")
	return contract.LimitStatus{InUse: inUse, Limit: limit, Saturated: inUse >= limit}, mapViewError(err)
}

func operationTransitionAllowed(from, to contract.ServerOperationState) bool {
	switch from {
	case contract.OperationScheduled:
		return to == contract.OperationRunning || to == contract.OperationCancelled ||
			to == contract.OperationSuperseded || to == contract.OperationInterrupted
	case contract.OperationRunning:
		return to == contract.OperationSucceeded || to == contract.OperationFailed ||
			to == contract.OperationCancelled || to == contract.OperationSuperseded ||
			to == contract.OperationInterrupted
	default:
		return false
	}
}

func operationTerminal(state contract.ServerOperationState) bool {
	return state != contract.OperationScheduled && state != contract.OperationRunning
}

func nullableReason(reason *contract.PublicReason) any {
	if reason == nil {
		return nil
	}
	return string(*reason)
}

func pruneTerminalOperationsTx(ctx context.Context, transaction *sql.Tx, serverID string) error {
	rows, err := transaction.QueryContext(ctx, `
		SELECT id FROM server_operations
		WHERE server_id = ? AND state NOT IN ('scheduled', 'running')
		ORDER BY insertion_sequence DESC, id DESC
		LIMIT -1 OFFSET ?`, serverID, mustLimit("terminal_operations"))
	if err != nil {
		return fmt.Errorf("select terminal operations for pruning: %w", err)
	}
	pruned := make([]string, 0)
	for rows.Next() {
		var operationID string
		if err := rows.Scan(&operationID); err != nil {
			_ = rows.Close()
			return err
		}
		pruned = append(pruned, operationID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(pruned) == 0 {
		return nil
	}
	for _, operationID := range pruned {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM server_operations WHERE id = ?`, operationID); err != nil {
			return fmt.Errorf("prune terminal operation: %w", err)
		}
	}
	if _, err := transaction.ExecContext(ctx, `
		UPDATE server_operation_watermarks
		SET pruning_generation = pruning_generation + 1
		WHERE server_id = ?`, serverID); err != nil {
		return fmt.Errorf("advance operation pruning generation: %w", err)
	}
	return nil
}

const operationSelect = `
	SELECT insertion_sequence, id, server_id, kind, target_desired_revision,
	       target_static_credential_revision, target_oauth_client_revision,
	       target_oauth_tokens_revision, state, reason, created_at, started_at, finished_at
	FROM server_operations`

func operationByIDTx(ctx context.Context, transaction *sql.Tx, operationID string) (Operation, error) {
	operation, err := scanOperation(transaction.QueryRowContext(ctx, operationSelect+` WHERE id = ?`, operationID))
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	return operation, err
}

func scanOperation(row scanner) (Operation, error) {
	var operation Operation
	var kind, state string
	var desiredRevision, staticRevision, oauthClientRevision, oauthTokensRevision int64
	var reason, startedAt, finishedAt sql.NullString
	if err := row.Scan(
		&operation.InsertionSequence,
		&operation.ID,
		&operation.ServerID,
		&kind,
		&desiredRevision,
		&staticRevision,
		&oauthClientRevision,
		&oauthTokensRevision,
		&state,
		&reason,
		&operation.CreatedAt,
		&startedAt,
		&finishedAt,
	); err != nil {
		return Operation{}, err
	}
	if _, err := parseTime(operation.CreatedAt); err != nil {
		return Operation{}, fmt.Errorf("parse operation creation time: %w", err)
	}
	operation.Kind = contract.ServerOperationKind(kind)
	operation.TargetDesiredRevision = strconv.FormatInt(desiredRevision, 10)
	operation.TargetCredentialRevisions = contract.CredentialRevisions{
		StaticCredential: strconv.FormatInt(staticRevision, 10),
		OAuthClient:      strconv.FormatInt(oauthClientRevision, 10),
		OAuthTokens:      strconv.FormatInt(oauthTokensRevision, 10),
	}
	operation.State = contract.ServerOperationState(state)
	if reason.Valid {
		value := contract.PublicReason(reason.String)
		operation.Reason = &value
	}
	if startedAt.Valid {
		if _, err := parseTime(startedAt.String); err != nil {
			return Operation{}, fmt.Errorf("parse operation start time: %w", err)
		}
		operation.StartedAt = &startedAt.String
	}
	if finishedAt.Valid {
		if _, err := parseTime(finishedAt.String); err != nil {
			return Operation{}, fmt.Errorf("parse operation finish time: %w", err)
		}
		operation.FinishedAt = &finishedAt.String
	}
	return operation, nil
}
