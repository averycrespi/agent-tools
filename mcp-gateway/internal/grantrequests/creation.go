package grantrequests

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

const requestTimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

type normalizedTarget struct {
	namespace    string
	externalName string
	upstreamName *string
}

type createOutcomeError struct {
	result contract.CreateGrantRequestResult
}

func (err *createOutcomeError) Error() string {
	return "grant request creation settled without mutation"
}

func (repository *Repository) CreateOrExisting(ctx context.Context, request CreateRequest) (contract.CreateGrantRequestResult, error) {
	if !opaqueIDPattern.MatchString(request.PrincipalID) {
		return contract.CreateGrantRequestResult{}, ErrInvalidInput
	}
	policy, err := CompilePolicy(request.Policy)
	if err != nil {
		return contract.CreateGrantRequestResult{}, ErrInvalidInput
	}
	target, err := normalizePolicyTarget(policy)
	if err != nil {
		return contract.CreateGrantRequestResult{}, err
	}
	var created contract.CreateGrantRequestResult
	mutationComplete := false
	err = repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		resolvedNamespace, lookupErr := repository.namespaces.LookupNamespaceTargetTx(ctx, transaction, target.namespace)
		if lookupErr != nil {
			if errors.Is(lookupErr, servers.ErrNotFound) {
				return createResult(contract.RequestTargetUnavailable, nil)
			}
			return fmt.Errorf("inspect request namespace: %w", lookupErr)
		}
		if !opaqueIDPattern.MatchString(resolvedNamespace.ID) || resolvedNamespace.ID == contract.SyntheticServerID ||
			resolvedNamespace.Namespace != target.namespace {
			return ErrInvalidState
		}
		if _, stateErr := contract.ParseDesiredServerState(string(resolvedNamespace.State)); stateErr != nil {
			return ErrInvalidState
		}
		resolved := ResolvedTarget{ServerID: resolvedNamespace.ID, UpstreamName: target.upstreamName}
		identity, identityErr := CanonicalDedupeIdentity(policy, resolved)
		if identityErr != nil {
			return ErrInvalidInput
		}
		existing, existingErr := loadPendingByIdentity(ctx, transaction, request.PrincipalID, identity, resolved)
		if existingErr != nil {
			return existingErr
		}
		if existing != nil {
			return createResult(contract.RequestExisting, existing)
		}
		if resolvedNamespace.State == contract.DesiredServerDeleted {
			return createResult(contract.RequestTargetUnavailable, nil)
		}

		now := repository.clock.Now()
		timestamp, valid := canonicalRequestTime(now)
		if !valid {
			return ErrIdentityUnavailable
		}
		var evidence []byte
		if policy.Scope() == contract.PolicyTool {
			descriptor, descriptorErr := repository.descriptors.LookupDurableDescriptorTx(ctx, transaction, resolved.ServerID, target.externalName)
			if descriptorErr != nil {
				if errors.Is(descriptorErr, servers.ErrNotFound) {
					return createResult(contract.RequestTargetUnavailable, nil)
				}
				return fmt.Errorf("inspect request descriptor: %w", descriptorErr)
			}
			if descriptor.Resource.ServerID != resolved.ServerID || descriptor.Resource.ExternalName != target.externalName ||
				descriptor.Resource.UpstreamName != *resolved.UpstreamName {
				return ErrInvalidState
			}
			_, evidence, descriptorErr = BuildDescriptorEvidence(descriptor, resolvedNamespace.Namespace, now)
			if descriptorErr != nil {
				return createResult(contract.RequestTargetUnavailable, nil)
			}
		}
		conflict, denyErr := repository.denies.HasActiveDenyConflictTx(ctx, transaction, authorization.DenyConflictScope{
			PrincipalID: request.PrincipalID, ServerID: resolved.ServerID, UpstreamName: resolved.UpstreamName,
		}, now.UTC())
		if denyErr != nil {
			return fmt.Errorf("inspect request DENY conflicts: %w", denyErr)
		}
		if conflict {
			return createResult(contract.RequestDenyConflict, nil)
		}
		if capacityErr := repository.ensureRequestCapacity(ctx, transaction, request.PrincipalID, int64(len(evidence))); capacityErr != nil {
			if errors.Is(capacityErr, errRequestCapacity) {
				return createResult(contract.RequestLimitReached, nil)
			}
			return capacityErr
		}

		repository.entropyMu.Lock()
		requestID, idErr := admin.NewID(now, repository.entropy)
		repository.entropyMu.Unlock()
		if idErr != nil {
			return ErrIdentityUnavailable
		}
		var collision int
		if queryErr := transaction.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM grant_request_identities WHERE id = ?)`, requestID).Scan(&collision); queryErr != nil {
			return fmt.Errorf("inspect request identity: %w", queryErr)
		}
		if collision != 0 {
			return ErrIdentityUnavailable
		}
		if _, insertErr := transaction.ExecContext(ctx, `INSERT INTO grant_request_identities (id, created_at) VALUES (?, ?)`, requestID, timestamp); insertErr != nil {
			return fmt.Errorf("insert request identity: %w", insertErr)
		}
		if insertErr := insertPendingRequest(ctx, transaction, requestID, request.PrincipalID, policy, resolved, identity, evidence, timestamp); insertErr != nil {
			return insertErr
		}
		resource := contract.AgentGrantRequest{
			ID: requestID, State: contract.RequestPending, Revision: "1", RequestedPolicy: policy.Contract(),
			CreatedAt: timestamp, UpdatedAt: timestamp,
		}
		created = contract.CreateGrantRequestResult{Outcome: contract.RequestCreated, Request: &resource}
		mutationComplete = true
		return nil
	})
	if err != nil {
		if mutationComplete {
			return contract.CreateGrantRequestResult{}, ErrStorageOutcomeUncertain
		}
		if errors.Is(err, storage.ErrStorageLatched) || errors.Is(err, storage.ErrMutationBusy) || repository.store.Latched() {
			return contract.CreateGrantRequestResult{}, ErrStorageUnavailable
		}
		var outcome *createOutcomeError
		if errors.As(err, &outcome) {
			return outcome.result, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return contract.CreateGrantRequestResult{}, err
		}
		if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrIdentityUnavailable) {
			return contract.CreateGrantRequestResult{}, err
		}
		return contract.CreateGrantRequestResult{}, fmt.Errorf("%w: create grant request: %w", ErrStorageUnavailable, err)
	}
	repository.invalidate(contract.Invalidation{Kind: contract.InvalidationGrantRequests})
	return created, nil
}

func createResult(outcome contract.CreateGrantRequestOutcome, request *contract.AgentGrantRequest) error {
	return &createOutcomeError{result: contract.CreateGrantRequestResult{Outcome: outcome, Request: request}}
}

func normalizePolicyTarget(policy CompiledPolicy) (normalizedTarget, error) {
	value := policy.Target()
	if policy.Scope() == contract.PolicyServer {
		if !validRequestNamespace(value) {
			return normalizedTarget{}, ErrInvalidInput
		}
		return normalizedTarget{namespace: value}, nil
	}
	separator := strings.IndexByte(value, '.')
	if separator < 1 || separator == len(value)-1 {
		return normalizedTarget{}, ErrInvalidInput
	}
	namespace, upstream := value[:separator], value[separator+1:]
	if !validRequestNamespace(namespace) || !validRequestToolName(upstream) {
		return normalizedTarget{}, ErrInvalidInput
	}
	return normalizedTarget{namespace: namespace, externalName: value, upstreamName: &upstream}, nil
}

func validRequestNamespace(value string) bool {
	if len(value) < 1 || len(value) > 32 {
		return false
	}
	for index, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || index > 0 && character >= '0' && character <= '9' ||
			index > 0 && (character == '_' || character == '-') {
			continue
		}
		return false
	}
	return true
}

func validRequestToolName(value string) bool {
	if len(value) < 1 || int64(len(value)) > fixedLimit("tool_name_bytes") {
		return false
	}
	for _, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func canonicalRequestTime(value time.Time) (string, bool) {
	if value.IsZero() || value.Year() < 0 || value.Year() > 9999 {
		return "", false
	}
	formatted := value.UTC().Format(requestTimestampLayout)
	parsed, err := time.Parse(requestTimestampLayout, formatted)
	return formatted, err == nil && parsed.Equal(value)
}

func insertPendingRequest(
	ctx context.Context,
	transaction *sql.Tx,
	requestID, principalID string,
	policy CompiledPolicy,
	resolved ResolvedTarget,
	identity DedupeIdentity,
	evidence []byte,
	timestamp string,
) error {
	var constraint, duration any
	if value := policy.ConstraintJSON(); value != nil {
		constraint = string(*value)
	}
	if _, present := policy.DurationSeconds(); present {
		duration = *policy.Contract().DurationSeconds
	}
	_, err := transaction.ExecContext(ctx, `INSERT INTO grant_requests (
		id, principal_id, state, revision, resolved_server_id, resolved_upstream_name,
		requested_scope, requested_target, requested_constraint, requested_duration_seconds,
		requested_future_tools_acknowledged, dedupe_version, dedupe_bytes, submitted_evidence,
		approved_scope, approved_target, approved_constraint, approved_duration_seconds,
		approved_future_tools_acknowledged, approved_grant_id, rejection_reason, approved_evidence,
		created_at, updated_at, closed_at
	) VALUES (?, ?, 'pending', 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, ?, ?, NULL)`,
		requestID, principalID, resolved.ServerID, resolved.UpstreamName, policy.Scope(), policy.Target(), constraint, duration,
		policy.FutureToolsAcknowledged(), identity.Version, identity.Bytes, nullableEvidence(evidence), timestamp, timestamp)
	if err != nil {
		return fmt.Errorf("insert pending grant request: %w", err)
	}
	return nil
}

func nullableEvidence(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func loadPendingByIdentity(
	ctx context.Context,
	transaction *sql.Tx,
	principalID string,
	identity DedupeIdentity,
	resolved ResolvedTarget,
) (*contract.AgentGrantRequest, error) {
	var (
		id, state, requestedScope, requestedTarget, createdAt, updatedAt string
		revision, dedupeVersion                                          int64
		requestedConstraint, requestedDuration                           sql.NullString
		requestedAcknowledged                                            bool
		dedupeBytes                                                      []byte
		approvedScope, approvedTarget, approvedConstraint                sql.NullString
		approvedDuration, approvedGrantID, rejectionReason               sql.NullString
		approvedAcknowledged                                             sql.NullBool
		closedAt                                                         sql.NullString
	)
	err := transaction.QueryRowContext(ctx, `SELECT
		id, state, revision, requested_scope, requested_target, requested_constraint,
		requested_duration_seconds, requested_future_tools_acknowledged,
		dedupe_version, dedupe_bytes,
		approved_scope, approved_target, approved_constraint, approved_duration_seconds,
		approved_future_tools_acknowledged, approved_grant_id, rejection_reason,
		created_at, updated_at, closed_at
	FROM grant_requests
	WHERE principal_id = ? AND state = 'pending' AND dedupe_version = ? AND dedupe_bytes = ?`,
		principalID, identity.Version, identity.Bytes).Scan(
		&id, &state, &revision, &requestedScope, &requestedTarget, &requestedConstraint,
		&requestedDuration, &requestedAcknowledged, &dedupeVersion, &dedupeBytes,
		&approvedScope, &approvedTarget, &approvedConstraint, &approvedDuration,
		&approvedAcknowledged, &approvedGrantID, &rejectionReason,
		&createdAt, &updatedAt, &closedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read duplicate grant request: %w", err)
	}
	if state != string(contract.RequestPending) || revision != 1 || dedupeVersion != identity.Version || !bytes.Equal(dedupeBytes, identity.Bytes) ||
		approvedScope.Valid || approvedTarget.Valid || approvedConstraint.Valid || approvedDuration.Valid || approvedAcknowledged.Valid ||
		approvedGrantID.Valid || rejectionReason.Valid || closedAt.Valid || createdAt != updatedAt || !validStoredRequestTime(createdAt) {
		return nil, ErrInvalidState
	}
	storedPolicy := contract.Policy{
		Scope: contract.PolicyScope(requestedScope), Target: requestedTarget,
		FutureToolsAcknowledged: requestedAcknowledged,
	}
	if requestedConstraint.Valid {
		value := jsonRaw(requestedConstraint.String)
		storedPolicy.Constraint = &value
	}
	if requestedDuration.Valid {
		value := requestedDuration.String
		storedPolicy.DurationSeconds = &value
	}
	compiled, compileErr := CompilePolicy(storedPolicy)
	if compileErr != nil {
		return nil, ErrInvalidState
	}
	recomputed, dedupeErr := CanonicalDedupeIdentity(compiled, resolved)
	if dedupeErr != nil || recomputed.Version != identity.Version || !bytes.Equal(recomputed.Bytes, identity.Bytes) {
		return nil, ErrInvalidState
	}
	return &contract.AgentGrantRequest{
		ID: id, State: contract.RequestPending, Revision: strconv.FormatInt(revision, 10),
		RequestedPolicy: compiled.Contract(), CreatedAt: createdAt, UpdatedAt: updatedAt,
	}, nil
}

func jsonRaw(value string) json.RawMessage { return json.RawMessage(value) }

func validStoredRequestTime(value string) bool {
	parsed, err := time.Parse(requestTimestampLayout, value)
	return err == nil && parsed.Format(requestTimestampLayout) == value
}
