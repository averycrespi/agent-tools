package servers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

const serverCollection = "servers"

func (repository *Repository) Create(ctx context.Context, request CreateRequest) (CreateResult, error) {
	transport, err := validateDefinition(request.Definition)
	if err != nil {
		return CreateResult{}, err
	}
	if err := validateIdempotencyRequest(request.Idempotency); err != nil {
		return CreateResult{}, err
	}
	serverID := request.ID
	if serverID == "" {
		serverID, err = repository.NewID()
		if err != nil {
			return CreateResult{}, err
		}
	}
	if !validID(serverID) {
		return CreateResult{}, fmt.Errorf("%w: server ID", ErrInvalidInput)
	}
	var operationID string
	if request.Definition.Enabled {
		operationID, err = repository.NewID()
		if err != nil {
			return CreateResult{}, err
		}
	}

	var result CreateResult
	err = repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		now := repository.clock.Now()
		stored, snapshot, found, lookupErr := lookupIdempotencyTx(ctx, transaction, request.Idempotency, now)
		if lookupErr != nil {
			return lookupErr
		}
		if found {
			result = CreateResult{Server: *snapshot.Server, Operation: snapshot.ServerMutationWork, Result: stored, Replayed: true}
			return nil
		}
		if err := admitIdempotencyTx(ctx, transaction, now); err != nil {
			return err
		}

		var idCount, namespaceCount int
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM server_identities WHERE id = ?`, serverID).Scan(&idCount); err != nil {
			return fmt.Errorf("check server ID: %w", err)
		}
		if idCount != 0 {
			return ErrIdentityUnavailable
		}
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM server_identities WHERE namespace = ?`, request.Definition.Namespace).Scan(&namespaceCount); err != nil {
			return fmt.Errorf("check server namespace: %w", err)
		}
		if namespaceCount != 0 {
			return ErrNamespaceUnavailable
		}
		if err := checkServerCreateCapacity(ctx, transaction, request.Definition.Enabled); err != nil {
			return err
		}

		createdAt := formatTime(now)
		state := contract.DesiredServerDisabled
		if request.Definition.Enabled {
			state = contract.DesiredServerEnabled
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO server_identities (id, namespace, created_at)
			VALUES (?, ?, ?)`, serverID, request.Definition.Namespace, createdAt); err != nil {
			return fmt.Errorf("insert server identity: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO servers (
				id, display_name, desired_state, desired_revision, transport_json,
				created_at, updated_at, deleted_at
			) VALUES (?, ?, ?, 1, ?, ?, ?, NULL)`,
			serverID, request.Definition.DisplayName, state, string(transport), createdAt, createdAt); err != nil {
			return fmt.Errorf("insert server definition: %w", err)
		}
		for _, kind := range []contract.ServerCredentialKind{
			contract.ServerCredentialStatic,
			contract.ServerCredentialOAuthClient,
			contract.ServerCredentialOAuthTokens,
		} {
			if _, err := transaction.ExecContext(ctx, `
				INSERT INTO server_credentials (server_id, kind, revision, handle)
				VALUES (?, ?, 0, NULL)`, serverID, kind); err != nil {
				return fmt.Errorf("initialize server credential metadata: %w", err)
			}
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO server_oauth_registrations (server_id, revision)
			VALUES (?, 0)`, serverID); err != nil {
			return fmt.Errorf("initialize OAuth registration fence: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO server_operation_watermarks (server_id, pruning_generation)
			VALUES (?, 0)`, serverID); err != nil {
			return fmt.Errorf("initialize operation watermark: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO server_auth_flow_watermarks (server_id, pruning_generation)
			VALUES (?, 0)`, serverID); err != nil {
			return fmt.Errorf("initialize auth-flow watermark: %w", err)
		}
		server, err := serverByIDTx(ctx, transaction, serverID)
		if err != nil {
			return err
		}
		var operation *Operation
		if request.Definition.Enabled {
			created, createErr := insertOperationTx(ctx, transaction, operationID, serverID, contract.OperationActivate, server.DesiredRevision, now)
			if createErr != nil {
				return createErr
			}
			operation = &created
		}
		stored = IdempotencyResult{Kind: "server", ServerID: serverID, DesiredRevision: "1"}
		if err := insertIdempotencyTx(ctx, transaction, request.Idempotency, stored, idempotencySnapshot{Server: &server, ServerMutationWork: operation}, now); err != nil {
			return err
		}
		result = CreateResult{Server: server, Operation: operation, Result: stored}
		return nil
	})
	return result, mapMutationError(err)
}

func (repository *Repository) Get(ctx context.Context, serverID string) (Server, error) {
	if !validID(serverID) {
		return Server{}, ErrNotFound
	}
	var server Server
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		var err error
		server, err = serverByIDTx(ctx, transaction, serverID)
		return err
	})
	return server, mapViewError(err)
}

func (repository *Repository) Patch(ctx context.Context, serverID, expectedRevision string, patch Patch) (PatchResult, error) {
	if !validID(serverID) {
		return PatchResult{}, ErrNotFound
	}
	expected, err := parseRevision(expectedRevision)
	if err != nil || expected < 1 {
		return PatchResult{}, ErrStaleRevision
	}
	if patch.DisplayName == nil && patch.Enabled == nil && patch.Transport == nil {
		return PatchResult{}, ErrInvalidInput
	}
	if patch.DisplayName != nil {
		if _, err := validateDefinition(Definition{Namespace: "valid", DisplayName: *patch.DisplayName, Transport: minimalStdioTransport()}); err != nil {
			return PatchResult{}, err
		}
	}
	var transport []byte
	if patch.Transport != nil {
		transport, err = canonicalTransport(patch.Transport)
		if err != nil {
			return PatchResult{}, err
		}
	}
	operationID, err := repository.NewID()
	if err != nil {
		return PatchResult{}, err
	}

	var result PatchResult
	err = repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		current, getErr := serverByIDTx(ctx, transaction, serverID)
		if getErr != nil {
			return getErr
		}
		currentRevision, parseErr := strconv.ParseInt(current.DesiredRevision, 10, 64)
		if parseErr != nil || currentRevision != expected {
			return ErrStaleRevision
		}
		if current.DesiredState == contract.DesiredServerDeleted {
			return ErrNotFound
		}
		displayName := current.DisplayName
		if patch.DisplayName != nil {
			displayName = *patch.DisplayName
		}
		state := current.DesiredState
		if patch.Enabled != nil {
			if *patch.Enabled && current.DesiredState != contract.DesiredServerEnabled {
				if err := checkEnabledCapacity(ctx, transaction); err != nil {
					return err
				}
				state = contract.DesiredServerEnabled
			} else if !*patch.Enabled {
				state = contract.DesiredServerDisabled
			}
		}
		transportValue := []byte(current.Transport)
		if patch.Transport != nil {
			transportValue = transport
		}
		behavioral := state != current.DesiredState || !bytes.Equal(transportValue, current.Transport)
		now := repository.clock.Now()
		if _, err := transaction.ExecContext(ctx, `
			UPDATE servers SET
				display_name = ?, desired_state = ?, desired_revision = desired_revision + 1,
				transport_json = ?, updated_at = ?
			WHERE id = ?`, displayName, state, string(transportValue), formatTime(now), serverID); err != nil {
			return fmt.Errorf("patch server definition: %w", err)
		}
		updated, getErr := serverByIDTx(ctx, transaction, serverID)
		if getErr != nil {
			return getErr
		}
		result.Server = updated
		if behavioral {
			if supersedeErr := supersedeNonterminalTx(ctx, transaction, serverID, now); supersedeErr != nil {
				return supersedeErr
			}
			if supersedeErr := supersedeAuthFlowsTx(ctx, transaction, serverID, now); supersedeErr != nil {
				return supersedeErr
			}
			kind := contract.OperationActivate
			if updated.DesiredState == contract.DesiredServerDisabled {
				kind = contract.OperationDisable
			}
			operation, operationErr := insertOperationTx(ctx, transaction, operationID, serverID, kind, updated.DesiredRevision, now)
			if operationErr != nil {
				return operationErr
			}
			result.Operation = &operation
		}
		return nil
	})
	return result, mapMutationError(err)
}

func (repository *Repository) Delete(ctx context.Context, serverID, expectedRevision string) (DeleteResult, error) {
	if !validID(serverID) {
		return DeleteResult{}, ErrNotFound
	}
	expected, err := parseRevision(expectedRevision)
	if err != nil || expected < 1 {
		return DeleteResult{}, ErrStaleRevision
	}
	operationID, err := repository.NewID()
	if err != nil {
		return DeleteResult{}, err
	}
	var result DeleteResult
	err = repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		current, getErr := serverByIDTx(ctx, transaction, serverID)
		if getErr != nil {
			return getErr
		}
		currentRevision, parseErr := strconv.ParseInt(current.DesiredRevision, 10, 64)
		if parseErr != nil || currentRevision != expected {
			return ErrStaleRevision
		}
		if current.DesiredState == contract.DesiredServerDeleted {
			operation, operationErr := operationForTargetTx(ctx, transaction, serverID, contract.OperationDelete, current.DesiredRevision)
			if operationErr != nil {
				return operationErr
			}
			result = DeleteResult{Server: current, Operation: &operation, Replayed: true}
			return nil
		}
		now := formatTime(repository.clock.Now())
		if _, err := transaction.ExecContext(ctx, `
			UPDATE servers SET desired_state = 'deleted', desired_revision = desired_revision + 1,
				transport_json = NULL, updated_at = ?, deleted_at = ?
			WHERE id = ?`, now, now, serverID); err != nil {
			return fmt.Errorf("tombstone server: %w", err)
		}
		current, getErr = serverByIDTx(ctx, transaction, serverID)
		if getErr != nil {
			return getErr
		}
		nowValue := repository.clock.Now()
		if supersedeErr := supersedeNonterminalTx(ctx, transaction, serverID, nowValue); supersedeErr != nil {
			return supersedeErr
		}
		if supersedeErr := supersedeAuthFlowsTx(ctx, transaction, serverID, nowValue); supersedeErr != nil {
			return supersedeErr
		}
		operation, operationErr := insertOperationTx(ctx, transaction, operationID, serverID, contract.OperationDelete, current.DesiredRevision, repository.clock.Now())
		if operationErr != nil {
			return operationErr
		}
		result = DeleteResult{Server: current, Operation: &operation}
		return nil
	})
	return result, mapMutationError(err)
}

func (repository *Repository) ListServers(ctx context.Context, cursor *SnapshotCursor, limit int) (ServerPage, error) {
	if err := validatePageLimit(limit); err != nil {
		return ServerPage{}, err
	}
	var page ServerPage
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		snapshot := SnapshotCursor{Collection: serverCollection}
		if cursor == nil {
			if err := transaction.QueryRowContext(ctx, `SELECT coalesce(max(insertion_sequence), 0) FROM server_identities`).Scan(&snapshot.Upper); err != nil {
				return fmt.Errorf("capture server insertion watermark: %w", err)
			}
		} else {
			snapshot = *cursor
			if snapshot.Collection != serverCollection || snapshot.ServerID != "" || snapshot.Floor != 0 || snapshot.After < 0 || snapshot.After > snapshot.Upper {
				return ErrStaleCursor
			}
		}
		rows, err := transaction.QueryContext(ctx, serverSelect+`
			WHERE i.insertion_sequence > ? AND i.insertion_sequence <= ?
			ORDER BY i.insertion_sequence, i.id LIMIT ?`, snapshot.After, snapshot.Upper, limit+1)
		if err != nil {
			return fmt.Errorf("list servers: %w", err)
		}
		defer func() { _ = rows.Close() }()
		items := make([]Server, 0, limit+1)
		for rows.Next() {
			server, scanErr := scanServer(rows)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, server)
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

func minimalStdioTransport() contract.StdioTransport {
	return contract.StdioTransport{
		Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{},
	}
}

func checkServerCreateCapacity(ctx context.Context, transaction *sql.Tx, enabled bool) error {
	var identities, servers int64
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM server_identities`).Scan(&identities); err != nil {
		return fmt.Errorf("count server identities: %w", err)
	}
	if identities >= mustLimit("server_identities") {
		return ErrResourceLimit
	}
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM servers WHERE desired_state <> 'deleted'`).Scan(&servers); err != nil {
		return fmt.Errorf("count nondeleted servers: %w", err)
	}
	if servers >= mustLimit("servers") {
		return ErrResourceLimit
	}
	if enabled {
		return checkEnabledCapacity(ctx, transaction)
	}
	return nil
}

func checkEnabledCapacity(ctx context.Context, transaction *sql.Tx) error {
	var enabled int64
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM servers WHERE desired_state = 'enabled'`).Scan(&enabled); err != nil {
		return fmt.Errorf("count enabled servers: %w", err)
	}
	if enabled >= mustLimit("enabled_servers") {
		return ErrResourceLimit
	}
	return nil
}

const serverSelect = `
	SELECT i.insertion_sequence, s.id, i.namespace, s.display_name, s.desired_state,
	       s.desired_revision, s.transport_json, s.created_at, s.updated_at, s.deleted_at
	FROM servers s JOIN server_identities i ON i.id = s.id`

func serverByIDTx(ctx context.Context, transaction *sql.Tx, serverID string) (Server, error) {
	server, err := scanServer(transaction.QueryRowContext(ctx, serverSelect+` WHERE s.id = ?`, serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return Server{}, ErrNotFound
	}
	return server, err
}

type scanner interface {
	Scan(...any) error
}

func scanServer(row scanner) (Server, error) {
	var server Server
	var state string
	var revision int64
	var transport sql.NullString
	var deletedAt sql.NullString
	if err := row.Scan(
		&server.InsertionSequence,
		&server.ID,
		&server.Namespace,
		&server.DisplayName,
		&state,
		&revision,
		&transport,
		&server.CreatedAt,
		&server.UpdatedAt,
		&deletedAt,
	); err != nil {
		return Server{}, err
	}
	if _, err := parseTime(server.CreatedAt); err != nil {
		return Server{}, fmt.Errorf("parse server creation time: %w", err)
	}
	if _, err := parseTime(server.UpdatedAt); err != nil {
		return Server{}, fmt.Errorf("parse server update time: %w", err)
	}
	server.DesiredState = contract.DesiredServerState(state)
	server.DesiredRevision = strconv.FormatInt(revision, 10)
	if transport.Valid {
		server.Transport = json.RawMessage(transport.String)
	}
	if deletedAt.Valid {
		if _, err := parseTime(deletedAt.String); err != nil {
			return Server{}, fmt.Errorf("parse server deletion time: %w", err)
		}
		server.DeletedAt = &deletedAt.String
	}
	return server, nil
}
