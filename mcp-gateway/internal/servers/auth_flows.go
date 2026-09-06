package servers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
)

const authFlowCollection = "server_auth_flows"

var ErrOAuthFlowActive = errors.New("OAuth flow is already exchanging")

type AuthFlow struct {
	Cause                 audit.Cause `json:"-"`
	InsertionSequence     int64
	ID                    string
	ServerID              string
	State                 contract.AuthFlowState
	TargetDesiredRevision string
	RegistrationRevision  string
	CreatedAt             string
	ExpiresAt             string
	FinishedAt            *string
	Reason                *contract.PublicReason
	Diagnostic            *contract.OAuthDiagnostic
}

type AuthFlowCreateRequest struct {
	ServerID                string
	ExpectedDesiredRevision string
}

type AuthFlowOAuthConfiguration struct {
	Resource       string
	Authentication contract.OAuthAuthentication
}

type AuthFlowCreateResult struct {
	Flow          AuthFlow
	Server        Server
	Authority     AuthorityMetadata
	Configuration AuthFlowOAuthConfiguration
	SupersededIDs []string
}

type AuthFlowPage struct {
	Items []AuthFlow
	Next  *SnapshotCursor
}

type OAuthTokenFence struct {
	ServerID                     string
	FlowID                       string
	ExpectedDesiredRevision      string
	ExpectedRegistrationRevision string
	ExpectedOAuthClientRevision  string
	ExpectedOAuthTokensRevision  string
}

func (repository *Repository) CreateAuthFlow(ctx context.Context, request AuthFlowCreateRequest) (AuthFlowCreateResult, error) {
	if !validID(request.ServerID) || request.ExpectedDesiredRevision == "" {
		return AuthFlowCreateResult{}, ErrInvalidInput
	}
	flowID, err := repository.NewID()
	if err != nil {
		return AuthFlowCreateResult{}, err
	}
	var result AuthFlowCreateResult
	err = repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		now := repository.clock.Now()
		server, err := serverByIDTx(ctx, transaction, request.ServerID)
		if err != nil {
			return err
		}
		if server.DesiredRevision != request.ExpectedDesiredRevision {
			return ErrStaleRevision
		}
		configuration, err := authFlowConfiguration(server)
		if err != nil {
			return err
		}
		authority, err := authorityTx(ctx, transaction, server.ID)
		if err != nil {
			return err
		}
		if err := validateAuthFlowAuthority(configuration.Authentication, authority); err != nil {
			return err
		}
		rows, err := transaction.QueryContext(ctx, authFlowSelect+`
			WHERE server_id = ? AND flow_state IN ('preparing', 'awaiting_callback', 'exchanging')
			ORDER BY insertion_sequence, id`, server.ID)
		if err != nil {
			return fmt.Errorf("list active auth flows: %w", err)
		}
		active := make([]AuthFlow, 0, 1)
		for rows.Next() {
			flow, scanErr := scanAuthFlow(rows)
			if scanErr != nil {
				_ = rows.Close()
				return scanErr
			}
			active = append(active, flow)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, flow := range active {
			if flow.State == contract.AuthFlowExchanging {
				return ErrOAuthFlowActive
			}
		}
		var globalActive int64
		if err := transaction.QueryRowContext(ctx, `
			SELECT count(*) FROM server_auth_flows
			WHERE flow_state IN ('preparing', 'awaiting_callback', 'exchanging')`).Scan(&globalActive); err != nil {
			return fmt.Errorf("count active auth flows: %w", err)
		}
		if globalActive-int64(len(active)) >= mustLimit("oauth_flows") {
			return ErrResourceLimit
		}
		if len(active) != 0 {
			finishedAt := formatTime(now)
			if _, err := transaction.ExecContext(ctx, `
				UPDATE server_auth_flows
				SET flow_state = 'superseded', reason = 'superseded', finished_at = ?
				WHERE server_id = ? AND flow_state IN ('preparing', 'awaiting_callback')`, finishedAt, server.ID); err != nil {
				return fmt.Errorf("supersede auth flow: %w", err)
			}
			for _, flow := range active {
				if err := auditAuthFlowTx(ctx, transaction, now, flow.ID, contract.AuthFlowSuperseded, nil); err != nil {
					return err
				}
				result.SupersededIDs = append(result.SupersededIDs, flow.ID)
			}
		}
		desiredRevision, err := strconv.ParseInt(server.DesiredRevision, 10, 64)
		if err != nil {
			return err
		}
		registrationRevision, err := strconv.ParseInt(authority.RegistrationRevision, 10, 64)
		if err != nil {
			return err
		}
		createdAt := formatTime(now)
		expiresAt := formatTime(now.Add(contract.OAuthFlowLifetime))
		cause, err := audit.EncodeCause(ctx, flowID)
		if err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO server_auth_flows (
				id, server_id, flow_state, target_desired_revision,
				registration_revision, created_at, expires_at, finished_at, reason, audit_cause
			) VALUES (?, ?, 'preparing', ?, ?, ?, ?, NULL, NULL, ?)`,
			flowID, server.ID, desiredRevision, registrationRevision, createdAt, expiresAt, cause,
		); err != nil {
			return fmt.Errorf("insert auth flow: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO server_auth_flow_watermarks (server_id, pruning_generation)
			VALUES (?, 0) ON CONFLICT(server_id) DO NOTHING`, server.ID); err != nil {
			return fmt.Errorf("initialize auth-flow watermark: %w", err)
		}
		if err := pruneTerminalAuthFlowsTx(ctx, transaction, server.ID); err != nil {
			return err
		}
		result.Flow, err = authFlowByIDTx(ctx, transaction, flowID)
		result.Server = server
		result.Authority = authority
		result.Configuration = configuration
		if err != nil {
			return err
		}
		return auditAuthFlowTx(ctx, transaction, now, flowID, contract.AuthFlowPreparing, nil)
	})
	return result, mapMutationError(err)
}

func authFlowConfiguration(server Server) (AuthFlowOAuthConfiguration, error) {
	if server.DesiredState == contract.DesiredServerDeleted {
		return AuthFlowOAuthConfiguration{}, ErrInvalidOperation
	}
	decoded, err := DecodeTransport(server.Transport)
	if err != nil {
		return AuthFlowOAuthConfiguration{}, ErrInvalidOperation
	}
	transport, ok := decoded.(contract.StreamableHTTPTransport)
	if !ok {
		return AuthFlowOAuthConfiguration{}, ErrInvalidOperation
	}
	authentication, ok := transport.Authentication.(contract.OAuthAuthentication)
	if !ok {
		return AuthFlowOAuthConfiguration{}, ErrInvalidOperation
	}
	return AuthFlowOAuthConfiguration{Resource: transport.URL, Authentication: authentication}, nil
}

func validateAuthFlowAuthority(authentication contract.OAuthAuthentication, authority AuthorityMetadata) error {
	switch registration := authentication.Registration.(type) {
	case contract.StaticOAuthRegistration:
		switch registration.TokenEndpointAuthMethod {
		case contract.TokenEndpointAuthClientSecretBasic, contract.TokenEndpointAuthClientSecretPost:
			if authority.OAuthClientHandle == nil || authority.CredentialRevisions.OAuthClient == "0" {
				return ErrInvalidOperation
			}
		case contract.TokenEndpointAuthNone:
			if authority.OAuthClientHandle != nil {
				return ErrInvalidOperation
			}
		default:
			return ErrInvalidOperation
		}
	case contract.DynamicOAuthRegistration:
		return nil
	default:
		return ErrInvalidOperation
	}
	return nil
}

func (repository *Repository) BeginAuthFlowExchange(ctx context.Context, fence OAuthTokenFence) (AuthFlow, error) {
	parsed, err := parseOAuthTokenFence(fence)
	if err != nil {
		return AuthFlow{}, err
	}
	var result AuthFlow
	err = repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		flow, err := authFlowByIDTx(ctx, transaction, fence.FlowID)
		if err != nil || flow.ServerID != fence.ServerID || flow.State != contract.AuthFlowAwaitingCallback {
			return ErrStaleRevision
		}
		expiresAt, err := parseTime(flow.ExpiresAt)
		if err != nil {
			return err
		}
		if !repository.clock.Now().Before(expiresAt) {
			finishedAt := formatTime(repository.clock.Now())
			if _, err := transaction.ExecContext(ctx, `UPDATE server_auth_flows SET flow_state = 'expired', reason = 'oauth_expired', finished_at = ? WHERE id = ?`, finishedAt, flow.ID); err != nil {
				return err
			}
			return ErrStaleRevision
		}
		if err := validateOAuthTokenFenceTx(ctx, transaction, fence, parsed, contract.AuthFlowAwaitingCallback, repository.clock.Now()); err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE server_auth_flows SET flow_state = 'exchanging' WHERE id = ?`, flow.ID); err != nil {
			return fmt.Errorf("begin OAuth token exchange: %w", err)
		}
		result, err = authFlowByIDTx(ctx, transaction, flow.ID)
		if err != nil {
			return err
		}
		return auditAuthFlowTx(ctx, transaction, repository.clock.Now(), flow.ID, contract.AuthFlowExchanging, nil)
	})
	return result, mapMutationError(err)
}

func (repository *Repository) ValidateAuthFlowExchange(ctx context.Context, fence OAuthTokenFence) error {
	parsed, err := parseOAuthTokenFence(fence)
	if err != nil {
		return err
	}
	err = repository.store.View(ctx, func(transaction *sql.Tx) error {
		return validateOAuthTokenFenceTx(ctx, transaction, fence, parsed, contract.AuthFlowExchanging, repository.clock.Now())
	})
	return mapViewError(err)
}

func (repository *Repository) OAuthTokenAuthorityCallback(fence OAuthTokenFence) (keyring.AuthorityCallback, error) {
	parsed, err := parseOAuthTokenFence(fence)
	if err != nil {
		return nil, err
	}
	base, err := repository.CredentialAuthorityCallback(CredentialFence{
		ServerID: fence.ServerID, Kind: contract.ServerCredentialOAuthTokens,
		ExpectedDesiredRevision: fence.ExpectedDesiredRevision, ExpectedCredentialRevision: fence.ExpectedOAuthTokensRevision,
		ExpectedRegistrationRevision: fence.ExpectedRegistrationRevision,
	})
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, transaction *sql.Tx, update keyring.AuthorityUpdate) (string, error) {
		if transaction == nil || update.Owner != fence.ServerID || update.Kind != keyring.RecordOAuthTokens {
			return "", ErrInvalidInput
		}
		exactInvalidation := update.ExactInvalidation && update.Handle == nil
		if !exactInvalidation {
			current := parsed
			if update.ExactPublishedRevision != "" {
				current.tokens++
			}
			if err := validateOAuthTokenFenceTx(ctx, transaction, fence, current, contract.AuthFlowExchanging, repository.clock.Now()); err != nil {
				return "", err
			}
		}
		revision, err := base(ctx, transaction, update)
		if err != nil {
			return "", err
		}
		if update.ActivateOnly {
			now := formatTime(repository.clock.Now())
			result, err := transaction.ExecContext(ctx, `
				UPDATE server_auth_flows SET flow_state = 'succeeded', reason = NULL, finished_at = ?
				WHERE id = ? AND server_id = ? AND flow_state = 'exchanging'`, now, fence.FlowID, fence.ServerID)
			if err != nil {
				return "", err
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				return "", ErrStaleRevision
			}
		}
		if exactInvalidation {
			reason := contract.ReasonOAuthRejected
			result, err := transaction.ExecContext(ctx, `
				UPDATE server_auth_flows SET flow_state = 'failed', reason = ?, finished_at = ?, diagnostic_stage = ?, diagnostic_http_status = NULL
				WHERE id = ? AND server_id = ? AND flow_state = 'exchanging'`, reason, formatTime(repository.clock.Now()), contract.OAuthDiagnosticCredentialInstallation, fence.FlowID, fence.ServerID)
			if err != nil {
				return "", err
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				return "", ErrStaleRevision
			}
		}
		if update.ActivateOnly {
			if err := auditAuthFlowTx(ctx, transaction, repository.clock.Now(), fence.FlowID, contract.AuthFlowSucceeded, nil); err != nil {
				return "", err
			}
		}
		if exactInvalidation {
			reason := contract.ReasonOAuthRejected
			if err := auditAuthFlowTx(ctx, transaction, repository.clock.Now(), fence.FlowID, contract.AuthFlowFailed, &reason); err != nil {
				return "", err
			}
		}
		return revision, nil
	}, nil
}

type parsedOAuthTokenFence struct {
	desired, registration, client, tokens int64
}

func parseOAuthTokenFence(fence OAuthTokenFence) (parsedOAuthTokenFence, error) {
	if !validID(fence.ServerID) || !validID(fence.FlowID) {
		return parsedOAuthTokenFence{}, ErrNotFound
	}
	values := []*int64{}
	parsed := parsedOAuthTokenFence{}
	values = append(values, &parsed.desired, &parsed.registration, &parsed.client, &parsed.tokens)
	texts := []string{fence.ExpectedDesiredRevision, fence.ExpectedRegistrationRevision, fence.ExpectedOAuthClientRevision, fence.ExpectedOAuthTokensRevision}
	for index, text := range texts {
		value, err := parseRevision(text)
		if err != nil || index == 0 && value < 1 {
			return parsedOAuthTokenFence{}, ErrStaleRevision
		}
		*values[index] = value
	}
	return parsed, nil
}

func validateOAuthTokenFenceTx(ctx context.Context, transaction *sql.Tx, fence OAuthTokenFence, parsed parsedOAuthTokenFence, expectedState contract.AuthFlowState, now time.Time) error {
	var desired, registration, client, tokens int64
	var desiredState contract.DesiredServerState
	if err := transaction.QueryRowContext(ctx, `SELECT desired_revision, desired_state FROM servers WHERE id = ?`, fence.ServerID).Scan(&desired, &desiredState); err != nil {
		return ErrStaleRevision
	}
	var secretExpires sql.NullString
	if err := transaction.QueryRowContext(ctx, `SELECT revision, client_secret_expires_at FROM server_oauth_registrations WHERE server_id = ?`, fence.ServerID).Scan(&registration, &secretExpires); err != nil {
		return ErrStaleRevision
	}
	if secretExpires.Valid {
		expiresAt, err := parseTime(secretExpires.String)
		if err != nil || !now.Before(expiresAt) {
			return ErrStaleRevision
		}
	}
	if err := transaction.QueryRowContext(ctx, `SELECT revision FROM server_credentials WHERE server_id = ? AND kind = 'oauth_client'`, fence.ServerID).Scan(&client); err != nil {
		return ErrStaleRevision
	}
	if err := transaction.QueryRowContext(ctx, `SELECT revision FROM server_credentials WHERE server_id = ? AND kind = 'oauth_tokens'`, fence.ServerID).Scan(&tokens); err != nil {
		return ErrStaleRevision
	}
	flow, err := authFlowByIDTx(ctx, transaction, fence.FlowID)
	if err != nil || flow.ServerID != fence.ServerID || flow.State != expectedState || flow.TargetDesiredRevision != fence.ExpectedDesiredRevision || flow.RegistrationRevision != fence.ExpectedRegistrationRevision {
		return ErrStaleRevision
	}
	if desiredState == contract.DesiredServerDeleted || desired != parsed.desired || registration != parsed.registration || client != parsed.client || tokens != parsed.tokens {
		return ErrStaleRevision
	}
	return nil
}

func (repository *Repository) MarkAuthFlowAwaiting(ctx context.Context, flowID, expectedDesiredRevision, registrationRevision string) (AuthFlow, error) {
	if !validID(flowID) || expectedDesiredRevision == "" || registrationRevision == "" {
		return AuthFlow{}, ErrInvalidInput
	}
	var result AuthFlow
	err := repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		flow, err := authFlowByIDTx(ctx, transaction, flowID)
		if err != nil {
			return err
		}
		if flow.State != contract.AuthFlowPreparing || flow.TargetDesiredRevision != expectedDesiredRevision {
			return ErrStaleRevision
		}
		server, err := serverByIDTx(ctx, transaction, flow.ServerID)
		if err != nil {
			return err
		}
		if server.DesiredRevision != expectedDesiredRevision || server.DesiredState == contract.DesiredServerDeleted {
			return ErrStaleRevision
		}
		authority, err := authorityTx(ctx, transaction, flow.ServerID)
		if err != nil {
			return err
		}
		if authority.RegistrationRevision != registrationRevision {
			return ErrStaleRevision
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE server_auth_flows
			SET flow_state = 'awaiting_callback', registration_revision = ?
			WHERE id = ?`, registrationRevision, flowID); err != nil {
			return fmt.Errorf("publish awaiting auth flow: %w", err)
		}
		result, err = authFlowByIDTx(ctx, transaction, flowID)
		if err != nil {
			return err
		}
		return auditAuthFlowTx(ctx, transaction, repository.clock.Now(), flowID, contract.AuthFlowAwaitingCallback, nil)
	})
	return result, mapMutationError(err)
}

func (repository *Repository) FailAuthFlow(ctx context.Context, flowID string, diagnostic contract.OAuthDiagnostic) (AuthFlow, error) {
	if !validID(flowID) || diagnostic.CorrelationID != flowID {
		return AuthFlow{}, ErrInvalidInput
	}
	if _, err := contract.ParseOAuthDiagnosticStage(string(diagnostic.Stage)); err != nil {
		return AuthFlow{}, ErrInvalidInput
	}
	if _, err := contract.ParsePublicReason(string(diagnostic.Reason)); err != nil {
		return AuthFlow{}, ErrInvalidInput
	}
	if diagnostic.HTTPStatus != nil && (*diagnostic.HTTPStatus < 100 || *diagnostic.HTTPStatus > 599) {
		return AuthFlow{}, ErrInvalidInput
	}
	var result AuthFlow
	err := repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		flow, err := authFlowByIDTx(ctx, transaction, flowID)
		if err != nil {
			return err
		}
		if !authFlowTransitionAllowed(flow.State, contract.AuthFlowFailed) {
			return ErrInvalidTransition
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE server_auth_flows
			SET flow_state = 'failed', reason = ?, finished_at = ?, diagnostic_stage = ?, diagnostic_http_status = ?
			WHERE id = ?`, diagnostic.Reason, formatTime(repository.clock.Now()), diagnostic.Stage, nullableInt(diagnostic.HTTPStatus), flowID); err != nil {
			return fmt.Errorf("fail auth flow: %w", err)
		}
		result, err = authFlowByIDTx(ctx, transaction, flowID)
		if err != nil {
			return err
		}
		if err := auditAuthFlowTx(ctx, transaction, repository.clock.Now(), flowID, contract.AuthFlowFailed, &diagnostic.Reason); err != nil {
			return err
		}
		return pruneTerminalAuthFlowsTx(ctx, transaction, flow.ServerID)
	})
	return result, mapMutationError(err)
}

func (repository *Repository) TransitionAuthFlow(ctx context.Context, flowID string, state contract.AuthFlowState, reason *contract.PublicReason) (AuthFlow, error) {
	if !validID(flowID) {
		return AuthFlow{}, ErrNotFound
	}
	if _, err := contract.ParseAuthFlowState(string(state)); err != nil || reason != nil && !validReason(*reason) {
		return AuthFlow{}, ErrInvalidInput
	}
	var result AuthFlow
	err := repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		flow, err := authFlowByIDTx(ctx, transaction, flowID)
		if err != nil {
			return err
		}
		if !authFlowTransitionAllowed(flow.State, state) {
			return ErrInvalidTransition
		}
		var finishedAt any
		if authFlowTerminal(state) {
			finishedAt = formatTime(repository.clock.Now())
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE server_auth_flows SET flow_state = ?, reason = ?, finished_at = ? WHERE id = ?`,
			state, nullableReason(reason), finishedAt, flowID); err != nil {
			return fmt.Errorf("transition auth flow: %w", err)
		}
		result, err = authFlowByIDTx(ctx, transaction, flowID)
		if err != nil {
			return err
		}
		if err := auditAuthFlowTx(ctx, transaction, repository.clock.Now(), flowID, state, reason); err != nil {
			return err
		}
		if authFlowTerminal(state) {
			return pruneTerminalAuthFlowsTx(ctx, transaction, flow.ServerID)
		}
		return nil
	})
	return result, mapMutationError(err)
}

func (repository *Repository) CancelAuthFlow(ctx context.Context, serverID, flowID string) (AuthFlow, error) {
	if !validID(serverID) || !validID(flowID) {
		return AuthFlow{}, ErrNotFound
	}
	var result AuthFlow
	err := repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		flow, err := authFlowByIDTx(ctx, transaction, flowID)
		if err != nil || flow.ServerID != serverID {
			return ErrNotFound
		}
		switch flow.State {
		case contract.AuthFlowExchanging:
			return ErrOAuthFlowActive
		case contract.AuthFlowPreparing, contract.AuthFlowAwaitingCallback:
			finishedAt := formatTime(repository.clock.Now())
			if _, err := transaction.ExecContext(ctx, `
				UPDATE server_auth_flows SET flow_state = 'cancelled', reason = 'cancelled', finished_at = ? WHERE id = ?`, finishedAt, flowID); err != nil {
				return fmt.Errorf("cancel auth flow: %w", err)
			}
			result, err = authFlowByIDTx(ctx, transaction, flowID)
			if err != nil {
				return err
			}
			if err := auditAuthFlowTx(ctx, transaction, repository.clock.Now(), flowID, contract.AuthFlowCancelled, nil); err != nil {
				return err
			}
			return pruneTerminalAuthFlowsTx(ctx, transaction, serverID)
		default:
			result = flow
			return nil
		}
	})
	return result, mapMutationError(err)
}

func (repository *Repository) GetAuthFlow(ctx context.Context, serverID, flowID string) (AuthFlow, error) {
	if !validID(serverID) || !validID(flowID) {
		return AuthFlow{}, ErrNotFound
	}
	var result AuthFlow
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		flow, err := authFlowByIDTx(ctx, transaction, flowID)
		if err != nil || flow.ServerID != serverID {
			return ErrNotFound
		}
		result = flow
		return nil
	})
	return result, mapViewError(err)
}

func (repository *Repository) ListAuthFlows(ctx context.Context, serverID string, cursor *SnapshotCursor, limit int) (AuthFlowPage, error) {
	if !validID(serverID) {
		return AuthFlowPage{}, ErrNotFound
	}
	if err := validatePageLimit(limit); err != nil {
		return AuthFlowPage{}, err
	}
	var page AuthFlowPage
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		if err := ensureServerExists(ctx, transaction, serverID); err != nil {
			return err
		}
		var floor int64
		if err := transaction.QueryRowContext(ctx, `SELECT pruning_generation FROM server_auth_flow_watermarks WHERE server_id = ?`, serverID).Scan(&floor); err != nil {
			return fmt.Errorf("read auth-flow pruning generation: %w", err)
		}
		snapshot := SnapshotCursor{Collection: authFlowCollection, ServerID: serverID, Floor: floor}
		if cursor == nil {
			if err := transaction.QueryRowContext(ctx, `SELECT coalesce(max(insertion_sequence), 0) FROM server_auth_flows WHERE server_id = ?`, serverID).Scan(&snapshot.Upper); err != nil {
				return err
			}
		} else {
			snapshot = *cursor
			if snapshot.Collection != authFlowCollection || snapshot.ServerID != serverID || snapshot.Floor != floor || snapshot.After < 0 || snapshot.After > snapshot.Upper {
				return ErrStaleCursor
			}
		}
		rows, err := transaction.QueryContext(ctx, authFlowSelect+`
			WHERE server_id = ? AND insertion_sequence > ? AND insertion_sequence <= ?
			ORDER BY insertion_sequence, id LIMIT ?`, serverID, snapshot.After, snapshot.Upper, limit+1)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		items := make([]AuthFlow, 0, limit+1)
		for rows.Next() {
			flow, scanErr := scanAuthFlow(rows)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, flow)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(items) > limit {
			last := items[limit-1]
			next := snapshot
			next.After, next.AfterID = last.InsertionSequence, last.ID
			page.Next = &next
			items = items[:limit]
		}
		page.Items = items
		return nil
	})
	return page, mapViewError(err)
}

func (repository *Repository) ExpireAuthFlows(ctx context.Context) ([]string, error) {
	now := formatTime(repository.clock.Now())
	var due int64
	if err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		return transaction.QueryRowContext(ctx, `
			SELECT count(*) FROM server_auth_flows
			WHERE flow_state IN ('preparing', 'awaiting_callback') AND expires_at <= ?`, now).Scan(&due)
	}); err != nil {
		return nil, mapViewError(err)
	}
	if due == 0 {
		return nil, nil
	}
	var expired []string
	err := repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		now := formatTime(repository.clock.Now())
		rows, err := transaction.QueryContext(ctx, `
			SELECT id, server_id FROM server_auth_flows
			WHERE flow_state IN ('preparing', 'awaiting_callback') AND expires_at <= ?
			ORDER BY server_id, insertion_sequence, id`, now)
		if err != nil {
			return err
		}
		servers := make(map[string]struct{})
		for rows.Next() {
			var id, serverID string
			if err := rows.Scan(&id, &serverID); err != nil {
				_ = rows.Close()
				return err
			}
			expired = append(expired, id)
			servers[serverID] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(expired) == 0 {
			return nil
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE server_auth_flows SET flow_state = 'expired', reason = 'oauth_expired', finished_at = ?
			WHERE flow_state IN ('preparing', 'awaiting_callback') AND expires_at <= ?`, now, now); err != nil {
			return err
		}
		for _, id := range expired {
			if err := auditAuthFlowTx(ctx, transaction, repository.clock.Now(), id, contract.AuthFlowExpired, nil); err != nil {
				return err
			}
		}
		for serverID := range servers {
			if err := pruneTerminalAuthFlowsTx(ctx, transaction, serverID); err != nil {
				return err
			}
		}
		return nil
	})
	return expired, mapMutationError(err)
}

func (repository *Repository) InterruptAuthFlows(ctx context.Context) error {
	return mapMutationError(repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		now := formatTime(repository.clock.Now())
		rows, err := transaction.QueryContext(ctx, `SELECT id, server_id FROM server_auth_flows WHERE flow_state IN ('preparing', 'awaiting_callback', 'exchanging')`)
		if err != nil {
			return err
		}
		var serverIDs, flowIDs []string
		for rows.Next() {
			var id, serverID string
			if err := rows.Scan(&id, &serverID); err != nil {
				_ = rows.Close()
				return err
			}
			serverIDs = append(serverIDs, serverID)
			flowIDs = append(flowIDs, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE server_auth_flows SET flow_state = 'interrupted', reason = 'interrupted', finished_at = ?
			WHERE flow_state IN ('preparing', 'awaiting_callback', 'exchanging')`, now); err != nil {
			return err
		}
		for _, id := range flowIDs {
			if err := auditAuthFlowTx(ctx, transaction, repository.clock.Now(), id, contract.AuthFlowInterrupted, nil); err != nil {
				return err
			}
		}
		for _, serverID := range serverIDs {
			if err := pruneTerminalAuthFlowsTx(ctx, transaction, serverID); err != nil {
				return err
			}
		}
		return nil
	}))
}

func (repository *Repository) AuthFlowStatus(ctx context.Context) (contract.LimitStatus, error) {
	var inUse int64
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		return transaction.QueryRowContext(ctx, `SELECT count(*) FROM server_auth_flows WHERE flow_state IN ('preparing', 'awaiting_callback', 'exchanging')`).Scan(&inUse)
	})
	limit := mustLimit("oauth_flows")
	return contract.LimitStatus{InUse: inUse, Limit: limit, Saturated: inUse >= limit}, mapViewError(err)
}

func supersedeAuthFlowsTx(ctx context.Context, transaction *sql.Tx, serverID string, now time.Time) error {
	rows, err := transaction.QueryContext(ctx, `
		UPDATE server_auth_flows SET flow_state = 'superseded', reason = 'superseded', finished_at = ?
		WHERE server_id = ? AND flow_state IN ('preparing', 'awaiting_callback', 'exchanging') RETURNING id`, formatTime(now), serverID)
	if err != nil {
		return fmt.Errorf("supersede auth flows: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := auditAuthFlowTx(ctx, transaction, now, id, contract.AuthFlowSuperseded, nil); err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return pruneTerminalAuthFlowsTx(ctx, transaction, serverID)
}

func (repository *Repository) RecordOAuthEvent(ctx context.Context, event contract.AuditEvent) error {
	if event.Category != "oauth" {
		return ErrInvalidInput
	}
	return audit.Append(ctx, repository.store, event)
}

func auditAuthFlowTx(ctx context.Context, tx *sql.Tx, now time.Time, id string, state contract.AuthFlowState, reason *contract.PublicReason) error {
	flow, err := authFlowByIDTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if state == contract.AuthFlowExpired || state == contract.AuthFlowInterrupted {
		ctx = audit.WithCause(ctx, flow.Cause)
	} else {
		ctx = audit.InheritCause(ctx, flow.Cause)
	}
	action, outcome := "", "succeeded"
	switch state {
	case contract.AuthFlowPreparing:
		action = "create"
	case contract.AuthFlowAwaitingCallback:
		action = "await_callback"
	case contract.AuthFlowExchanging:
		action = "begin_exchange"
	case contract.AuthFlowSucceeded:
		action = "install"
	case contract.AuthFlowFailed:
		action, outcome = "finish", "failed"
	case contract.AuthFlowCancelled:
		action = "cancel"
	case contract.AuthFlowExpired:
		action = "expire"
	case contract.AuthFlowSuperseded:
		action = "supersede"
	case contract.AuthFlowInterrupted:
		action, outcome = "recover", "unknown"
	default:
		return ErrInvalidTransition
	}
	if state != contract.AuthFlowPreparing && state != contract.AuthFlowCancelled {
		ctx = audit.WithSystem(ctx)
	}
	return audit.MutationOutcomeTx(ctx, tx, now, "oauth", action, contract.AuditTarget{Type: "auth_flow", ID: id}, outcome, contract.AuditDetail{Reason: reason})
}

func pruneTerminalAuthFlowsTx(ctx context.Context, transaction *sql.Tx, serverID string) error {
	limit := mustLimit("terminal_auth_flows")
	rows, err := transaction.QueryContext(ctx, `
		SELECT id FROM server_auth_flows
		WHERE server_id = ? AND flow_state NOT IN ('preparing', 'awaiting_callback', 'exchanging')
		ORDER BY insertion_sequence DESC, id DESC LIMIT -1 OFFSET ?`, serverID, limit)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM server_auth_flows WHERE id = ?`, id); err != nil {
			return err
		}
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE server_auth_flow_watermarks SET pruning_generation = pruning_generation + 1 WHERE server_id = ?`, serverID); err != nil {
		return err
	}
	return nil
}

const authFlowSelect = `
	SELECT insertion_sequence, id, server_id, flow_state, target_desired_revision,
	       registration_revision, created_at, expires_at, finished_at, reason,
	       diagnostic_stage, diagnostic_http_status, audit_cause
	FROM server_auth_flows`

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func authFlowByIDTx(ctx context.Context, transaction *sql.Tx, flowID string) (AuthFlow, error) {
	return scanAuthFlow(transaction.QueryRowContext(ctx, authFlowSelect+` WHERE id = ?`, flowID))
}

type authFlowScanner interface{ Scan(...any) error }

func scanAuthFlow(scanner authFlowScanner) (AuthFlow, error) {
	var flow AuthFlow
	var desiredRevision, registrationRevision int64
	var state string
	var finishedAt, reason, diagnosticStage, cause sql.NullString
	var diagnosticHTTPStatus sql.NullInt64
	if err := scanner.Scan(&flow.InsertionSequence, &flow.ID, &flow.ServerID, &state, &desiredRevision, &registrationRevision, &flow.CreatedAt, &flow.ExpiresAt, &finishedAt, &reason, &diagnosticStage, &diagnosticHTTPStatus, &cause); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AuthFlow{}, ErrNotFound
		}
		return AuthFlow{}, err
	}
	if cause.Valid {
		var err error
		flow.Cause, err = audit.DecodeCause(cause.String)
		if err != nil {
			return AuthFlow{}, err
		}
	}
	parsedState, err := contract.ParseAuthFlowState(state)
	if err != nil {
		return AuthFlow{}, err
	}
	flow.State = parsedState
	flow.TargetDesiredRevision = strconv.FormatInt(desiredRevision, 10)
	flow.RegistrationRevision = strconv.FormatInt(registrationRevision, 10)
	if finishedAt.Valid {
		flow.FinishedAt = &finishedAt.String
	}
	if reason.Valid {
		parsed, err := contract.ParsePublicReason(reason.String)
		if err != nil {
			return AuthFlow{}, err
		}
		flow.Reason = &parsed
	}
	if diagnosticStage.Valid {
		stage, err := contract.ParseOAuthDiagnosticStage(diagnosticStage.String)
		if err != nil || flow.Reason == nil {
			return AuthFlow{}, ErrInvalidInput
		}
		var status *int
		if diagnosticHTTPStatus.Valid {
			value := int(diagnosticHTTPStatus.Int64)
			status = &value
		}
		flow.Diagnostic = &contract.OAuthDiagnostic{CorrelationID: flow.ID, Stage: stage, Reason: *flow.Reason, HTTPStatus: status}
	}
	return flow, nil
}

func authFlowTransitionAllowed(from, to contract.AuthFlowState) bool {
	switch from {
	case contract.AuthFlowPreparing:
		return to == contract.AuthFlowAwaitingCallback || authFlowTerminal(to)
	case contract.AuthFlowAwaitingCallback:
		return to == contract.AuthFlowExchanging || authFlowTerminal(to)
	case contract.AuthFlowExchanging:
		return to == contract.AuthFlowSucceeded || to == contract.AuthFlowFailed || to == contract.AuthFlowInterrupted
	default:
		return false
	}
}

func authFlowTerminal(state contract.AuthFlowState) bool {
	return state == contract.AuthFlowSucceeded || state == contract.AuthFlowFailed || state == contract.AuthFlowExpired || state == contract.AuthFlowCancelled || state == contract.AuthFlowSuperseded || state == contract.AuthFlowInterrupted
}

func validReason(reason contract.PublicReason) bool {
	_, err := contract.ParsePublicReason(string(reason))
	return err == nil
}
