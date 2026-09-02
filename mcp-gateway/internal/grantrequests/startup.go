package grantrequests

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/gowebpki/jcs"
)

// StoredPrincipalInspector exposes only permanent principal existence on the caller transaction.
type StoredPrincipalInspector interface {
	StoredPrincipalExistsTx(context.Context, *sql.Tx, string) (bool, error)
}

// StoredTargetInspector exposes only immutable target namespaces on the caller transaction.
type StoredTargetInspector interface {
	LookupStoredGrantNamespaceTx(context.Context, *sql.Tx, string) (string, bool, error)
}

type startupRequest struct {
	sequence          int64
	request           contract.AgentGrantRequest
	principalID       string
	serverID          string
	upstreamName      *string
	dedupeVersion     int64
	dedupeBytes       []byte
	submittedEvidence []byte
	approvedEvidence  []byte
	identityCreatedAt string
}

// ValidateStartup validates every retained request and permanent identity before readiness.
func (repository *Repository) ValidateStartup(ctx context.Context, principals StoredPrincipalInspector, targets StoredTargetInspector) error {
	if repository == nil {
		return ErrInvalidState
	}
	return ValidateStartup(ctx, repository.store, principals, targets)
}

func ValidateStartup(ctx context.Context, store *storage.Store, principals StoredPrincipalInspector, targets StoredTargetInspector) error {
	if store == nil || principals == nil || targets == nil {
		return ErrInvalidState
	}
	repository := &Repository{store: store}
	return repository.view(ctx, func(transaction *sql.Tx) error {
		if err := validateRequestIdentities(ctx, transaction); err != nil {
			return err
		}
		safe, sequences, err := loadSafeStartupRequests(ctx, transaction)
		if err != nil {
			return err
		}
		rows, err := transaction.QueryContext(ctx, `SELECT
			request.insertion_sequence, request.id, request.principal_id,
			request.resolved_server_id, request.resolved_upstream_name,
			request.dedupe_version, request.dedupe_bytes,
			request.submitted_evidence, request.approved_evidence, identity.created_at
			FROM grant_requests AS request
			LEFT JOIN grant_request_identities AS identity ON identity.id = request.id
			ORDER BY request.insertion_sequence, request.id LIMIT ?`, fixedLimit("grant_requests")+1)
		if err != nil {
			return fmt.Errorf("read grant requests for startup validation: %w", err)
		}
		defer func() { _ = rows.Close() }()
		principalCache := make(map[string]bool)
		targetCache := make(map[string]string)
		pendingByPrincipal := make(map[string]int64)
		var count, evidenceBytes int64
		for rows.Next() {
			stored, scanErr := scanStartupRequest(rows, safe, sequences)
			if scanErr != nil {
				return scanErr
			}
			if count >= fixedLimit("grant_requests") {
				return ErrInvalidState
			}
			count++
			if stored.identityCreatedAt != stored.request.CreatedAt {
				return ErrInvalidState
			}
			if stored.request.State == contract.RequestPending {
				pendingByPrincipal[stored.principalID]++
				if pendingByPrincipal[stored.principalID] > fixedLimit("pending_grant_requests_per_principal") {
					return ErrInvalidState
				}
			}
			exists, inspected := principalCache[stored.principalID]
			if !inspected {
				exists, err = principals.StoredPrincipalExistsTx(ctx, transaction, stored.principalID)
				if err != nil {
					return fmt.Errorf("inspect grant request owner: %w", err)
				}
				principalCache[stored.principalID] = exists
			}
			if !exists {
				return ErrInvalidState
			}
			namespace, inspected := targetCache[stored.serverID]
			if !inspected {
				var found bool
				namespace, found, err = targets.LookupStoredGrantNamespaceTx(ctx, transaction, stored.serverID)
				if err != nil {
					return fmt.Errorf("inspect grant request target: %w", err)
				}
				if !found {
					return ErrInvalidState
				}
				targetCache[stored.serverID] = namespace
			}
			if err := validateStartupRequest(stored, namespace); err != nil {
				return err
			}
			evidenceBytes += int64(len(stored.submittedEvidence) + len(stored.approvedEvidence))
			if evidenceBytes > fixedLimit("grant_request_evidence_bytes") {
				return ErrInvalidState
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate grant requests for startup validation: %w", err)
		}
		if count != int64(len(safe)) {
			return ErrInvalidState
		}
		var aggregate int64
		if err := transaction.QueryRowContext(ctx, `SELECT total_bytes FROM grant_request_evidence_bytes WHERE singleton = 1`).Scan(&aggregate); err != nil {
			return fmt.Errorf("read grant request evidence aggregate: %w", err)
		}
		if aggregate != evidenceBytes {
			return ErrInvalidState
		}
		return nil
	})
}

func validateRequestIdentities(ctx context.Context, transaction *sql.Tx) error {
	rows, err := transaction.QueryContext(ctx, `SELECT id, created_at FROM grant_request_identities ORDER BY id`)
	if err != nil {
		return fmt.Errorf("read grant request identities: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, createdAt string
		if err := rows.Scan(&id, &createdAt); err != nil {
			return fmt.Errorf("scan grant request identity: %w", err)
		}
		if !opaqueIDPattern.MatchString(id) || !validStoredRequestTime(createdAt) {
			return ErrInvalidState
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate grant request identities: %w", err)
	}
	return nil
}

func loadSafeStartupRequests(ctx context.Context, transaction *sql.Tx) (map[string]contract.AgentGrantRequest, map[string]int64, error) {
	rows, err := transaction.QueryContext(ctx, agentRequestSelect+` ORDER BY insertion_sequence, id LIMIT ?`, fixedLimit("grant_requests")+1)
	if err != nil {
		return nil, nil, fmt.Errorf("read safe grant request projections for validation: %w", err)
	}
	defer func() { _ = rows.Close() }()
	safe := make(map[string]contract.AgentGrantRequest)
	sequences := make(map[string]int64)
	var previous int64
	for rows.Next() {
		sequence, request, scanErr := scanAgentRequest(rows)
		if scanErr != nil || sequence <= previous || int64(len(safe)) >= fixedLimit("grant_requests") {
			return nil, nil, ErrInvalidState
		}
		previous = sequence
		safe[request.ID], sequences[request.ID] = request, sequence
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate safe grant request projections for validation: %w", err)
	}
	return safe, sequences, nil
}

func scanStartupRequest(rows *sql.Rows, safe map[string]contract.AgentGrantRequest, sequences map[string]int64) (startupRequest, error) {
	var (
		stored                              startupRequest
		requestID                           string
		upstreamName                        sql.NullString
		submittedEvidence, approvedEvidence []byte
		identityCreatedAt                   sql.NullString
	)
	if err := rows.Scan(
		&stored.sequence, &requestID, &stored.principalID, &stored.serverID, &upstreamName,
		&stored.dedupeVersion, &stored.dedupeBytes, &submittedEvidence, &approvedEvidence, &identityCreatedAt,
	); err != nil {
		return startupRequest{}, fmt.Errorf("scan grant request for validation: %w", err)
	}
	request, exists := safe[requestID]
	if !exists || sequences[requestID] != stored.sequence || !opaqueIDPattern.MatchString(stored.principalID) ||
		!opaqueIDPattern.MatchString(stored.serverID) || stored.serverID == contract.SyntheticServerID {
		return startupRequest{}, ErrInvalidState
	}
	if !identityCreatedAt.Valid {
		return startupRequest{}, ErrInvalidState
	}
	stored.request, stored.identityCreatedAt = request, identityCreatedAt.String
	if upstreamName.Valid {
		value := upstreamName.String
		stored.upstreamName = &value
	}
	stored.dedupeBytes = append([]byte(nil), stored.dedupeBytes...)
	stored.submittedEvidence = append([]byte(nil), submittedEvidence...)
	stored.approvedEvidence = append([]byte(nil), approvedEvidence...)
	return stored, nil
}

func validateStartupRequest(stored startupRequest, namespace string) error {
	requested, err := CompilePolicy(stored.request.RequestedPolicy)
	if err != nil {
		return ErrInvalidState
	}
	requestedName, err := normalizePolicyTarget(requested)
	if err != nil || requestedName.namespace != namespace || !equalOptionalString(requestedName.upstreamName, stored.upstreamName) {
		return ErrInvalidState
	}
	resolved := ResolvedTarget{ServerID: stored.serverID, UpstreamName: stored.upstreamName}
	identity, err := CanonicalDedupeIdentity(requested, resolved)
	if err != nil || stored.dedupeVersion != identity.Version || !bytes.Equal(stored.dedupeBytes, identity.Bytes) {
		return ErrInvalidState
	}
	if requested.Scope() == contract.PolicyTool {
		if err := validateStoredEvidence(stored.submittedEvidence, stored.serverID, requestedName, stored.request.CreatedAt); err != nil {
			return err
		}
	} else if len(stored.submittedEvidence) != 0 {
		return ErrInvalidState
	}

	needsApprovedEvidence := false
	var approvedName normalizedTarget
	if stored.request.ApprovedPolicy != nil {
		approved, compileErr := CompilePolicy(*stored.request.ApprovedPolicy)
		if compileErr != nil {
			return ErrInvalidState
		}
		approvedName, compileErr = normalizePolicyTarget(approved)
		if compileErr != nil || approvedName.namespace != namespace {
			return ErrInvalidState
		}
		needsApprovedEvidence = requested.Scope() == contract.PolicyServer && approved.Scope() == contract.PolicyTool
	}
	if needsApprovedEvidence {
		if stored.request.ClosedAt == nil || validateStoredEvidence(stored.approvedEvidence, stored.serverID, approvedName, *stored.request.ClosedAt) != nil {
			return ErrInvalidState
		}
	} else if len(stored.approvedEvidence) != 0 {
		return ErrInvalidState
	}
	return nil
}

func validateStoredEvidence(contents []byte, serverID string, target normalizedTarget, capturedAt string) error {
	if len(contents) == 0 || target.upstreamName == nil {
		return ErrInvalidState
	}
	var evidence contract.DescriptorEvidence
	if err := strictjson.Decode(contents, &evidence, strictjson.Options{
		MaxBytes: fixedLimit("grant_request_evidence_snapshot_bytes"), MaxDepth: 64, RejectUnknownMembers: true,
	}); err != nil {
		return ErrInvalidState
	}
	canonical, err := jcs.Transform(contents)
	if err != nil || !bytes.Equal(canonical, contents) || evidence.ServerID != serverID || evidence.Namespace != target.namespace ||
		evidence.UpstreamName != *target.upstreamName || evidence.ExternalName != target.externalName || evidence.CapturedAt != capturedAt {
		return ErrInvalidState
	}
	captured, valid := canonicalStoredTime(evidence.CapturedAt)
	if !valid {
		return ErrInvalidState
	}
	resource := contract.ToolDescriptor{
		ID: evidence.ToolID, ServerID: evidence.ServerID, UpstreamName: evidence.UpstreamName,
		ExternalName: evidence.ExternalName, CatalogRevision: evidence.CatalogRevision,
		Fingerprint: evidence.Fingerprint, Descriptor: evidence.Descriptor,
	}
	if evidence.DurableState == contract.EvidenceRetired {
		retired := evidence.CapturedAt
		resource.RetiredAt = &retired
	}
	_, rebuilt, err := BuildDescriptorEvidence(catalog.DurableDescriptor{Resource: resource, State: evidence.DurableState}, evidence.Namespace, captured)
	if err != nil || !bytes.Equal(rebuilt, contents) {
		return ErrInvalidState
	}
	return nil
}

func equalOptionalString(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
