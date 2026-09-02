package grantrequests

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

const adminRequestCollection = "grant_requests"

type adminPosition struct {
	sequence    int64
	id          string
	principalID string
}

func (repository *Repository) ListAdmin(ctx context.Context, filter AdminFilter, cursor *AdminCursor, limit int) (AdminPage, error) {
	if int64(limit) < 1 || int64(limit) > fixedLimit("admin_list_page") ||
		filter.PrincipalID != "" && !opaqueIDPattern.MatchString(filter.PrincipalID) {
		return AdminPage{}, ErrInvalidInput
	}
	if filter.State != nil {
		if _, err := contract.ParseGrantRequestState(string(*filter.State)); err != nil {
			return AdminPage{}, ErrInvalidInput
		}
	}
	var page AdminPage
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		position := AdminCursor{Collection: adminRequestCollection, PrincipalID: filter.PrincipalID, State: cloneRequestState(filter.State)}
		if cursor == nil {
			query, args := adminWatermarkQuery(filter)
			if err := transaction.QueryRowContext(ctx, query, args...).Scan(&position.Upper); err != nil {
				return fmt.Errorf("capture grant request admin watermark: %w", err)
			}
		} else {
			position = *cursor
			if !validAdminCursor(position, filter) {
				return ErrStaleCursor
			}
		}
		query, args := adminPageQuery(filter, position, limit+1)
		rows, err := transaction.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("list grant requests for administration: %w", err)
		}
		positions := make([]adminPosition, 0, limit+1)
		for rows.Next() {
			var item adminPosition
			if err := rows.Scan(&item.sequence, &item.id, &item.principalID); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan grant request admin page: %w", err)
			}
			if item.sequence < 1 || !opaqueIDPattern.MatchString(item.id) || !opaqueIDPattern.MatchString(item.principalID) {
				_ = rows.Close()
				return ErrInvalidState
			}
			positions = append(positions, item)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate grant request admin page: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close grant request admin page: %w", err)
		}
		if len(positions) > limit {
			last := positions[limit-1]
			next := position
			next.After, next.AfterID = last.sequence, last.id
			page.Next = &next
			positions = positions[:limit]
		}
		page.Items = make([]contract.GrantRequestSummary, 0, len(positions))
		for _, item := range positions {
			_, request, err := scanAgentRequest(transaction.QueryRowContext(ctx, agentRequestSelect+` WHERE id = ?`, item.id))
			if err != nil {
				return err
			}
			page.Items = append(page.Items, requestSummary(item.principalID, request))
		}
		return nil
	})
	return page, err
}

func (repository *Repository) GetAdmin(ctx context.Context, requestID string) (contract.GrantRequest, error) {
	if !opaqueIDPattern.MatchString(requestID) {
		return contract.GrantRequest{}, ErrNotFound
	}
	var result contract.GrantRequest
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		_, request, err := scanAgentRequest(transaction.QueryRowContext(ctx, agentRequestSelect+` WHERE id = ?`, requestID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var principalID, serverID string
		var upstream sql.NullString
		var submitted, approved []byte
		if err := transaction.QueryRowContext(ctx, `SELECT principal_id, resolved_server_id, resolved_upstream_name,
			submitted_evidence, approved_evidence FROM grant_requests WHERE id = ?`, requestID).
			Scan(&principalID, &serverID, &upstream, &submitted, &approved); err != nil {
			return fmt.Errorf("read grant request admin facts: %w", err)
		}
		result.GrantRequestSummary = requestSummary(principalID, request)
		result.ResolvedServerID = serverID
		if upstream.Valid {
			value := upstream.String
			result.ResolvedUpstreamName = &value
		}
		if len(submitted) != 0 {
			evidence, err := decodeAdminEvidence(submitted, serverID, request.RequestedPolicy, request.CreatedAt)
			if err != nil {
				return err
			}
			result.SubmittedEvidence = &evidence
		}
		if len(approved) != 0 {
			if request.ApprovedPolicy == nil || request.ClosedAt == nil {
				return ErrInvalidState
			}
			evidence, err := decodeAdminEvidence(approved, serverID, *request.ApprovedPolicy, *request.ClosedAt)
			if err != nil {
				return err
			}
			result.ApprovedEvidence = &evidence
		}
		comparison, err := repository.compareAdminTarget(ctx, transaction, result)
		if err != nil {
			return err
		}
		result.CurrentTarget = comparison
		return nil
	})
	return result, err
}

func requestSummary(principalID string, request contract.AgentGrantRequest) contract.GrantRequestSummary {
	return contract.GrantRequestSummary{
		ID: request.ID, PrincipalID: principalID, State: request.State, Revision: request.Revision,
		RequestedPolicy: request.RequestedPolicy, ApprovedPolicy: request.ApprovedPolicy,
		ApprovedGrantID: request.ApprovedGrantID, RejectionReason: request.RejectionReason,
		CreatedAt: request.CreatedAt, UpdatedAt: request.UpdatedAt, ClosedAt: request.ClosedAt,
	}
}

func (repository *Repository) compareAdminTarget(ctx context.Context, transaction *sql.Tx, request contract.GrantRequest) (contract.TargetComparison, error) {
	policy := request.RequestedPolicy
	if request.ApprovedPolicy != nil {
		policy = *request.ApprovedPolicy
	}
	compiled, err := CompilePolicy(policy)
	if err != nil {
		return contract.TargetComparison{}, ErrInvalidState
	}
	target, err := normalizePolicyTarget(compiled)
	if err != nil {
		return contract.TargetComparison{}, ErrInvalidState
	}
	namespace, err := repository.namespaces.LookupNamespaceTargetTx(ctx, transaction, target.namespace)
	if err != nil {
		return contract.TargetComparison{}, fmt.Errorf("compare grant request namespace: %w", err)
	}
	if namespace.ID != request.ResolvedServerID || namespace.Namespace != target.namespace || namespace.ID == contract.SyntheticServerID {
		return contract.TargetComparison{}, ErrInvalidState
	}
	if _, err := contract.ParseDesiredServerState(string(namespace.State)); err != nil {
		return contract.TargetComparison{}, ErrInvalidState
	}
	state := contract.TargetExtant
	if namespace.State == contract.DesiredServerDeleted {
		state = contract.TargetDeleted
	}
	comparison := contract.TargetComparison{Scope: policy.Scope, TargetState: state}
	if policy.Scope == contract.PolicyServer {
		return comparison, nil
	}
	active := contract.TargetActiveUnavailable
	durableState := contract.TargetDurableAbsent
	comparison.ActiveState, comparison.DurableState = &active, &durableState
	descriptor, err := repository.descriptors.LookupDurableDescriptorTx(ctx, transaction, request.ResolvedServerID, target.externalName)
	switch {
	case err == nil:
		if descriptor.Resource.ServerID != request.ResolvedServerID || descriptor.Resource.ExternalName != target.externalName ||
			descriptor.Resource.UpstreamName != *target.upstreamName {
			return contract.TargetComparison{}, ErrInvalidState
		}
		if _, parseErr := contract.ParseDescriptorEvidenceState(string(descriptor.State)); parseErr != nil {
			return contract.TargetComparison{}, ErrInvalidState
		}
		durableState = contract.TargetDurableCurrent
		if descriptor.State == contract.EvidenceRetired {
			durableState = contract.TargetDurableRetired
		}
		comparison.DurableState = &durableState
		revision, fingerprint, normalized := descriptor.Resource.CatalogRevision, descriptor.Resource.Fingerprint, descriptor.Resource.Descriptor
		comparison.CatalogRevision, comparison.Fingerprint, comparison.Descriptor = &revision, &fingerprint, &normalized
		if repository.active != nil {
			active = repository.active.CompareActiveTarget(ctx, request.ResolvedServerID, *target.upstreamName, fingerprint)
			if _, parseErr := contract.ParseTargetActiveState(string(active)); parseErr != nil {
				return contract.TargetComparison{}, ErrInvalidState
			}
			comparison.ActiveState = &active
		}
	case errors.Is(err, servers.ErrNotFound):
		if repository.active != nil {
			active = repository.active.CompareActiveTarget(ctx, request.ResolvedServerID, *target.upstreamName, "")
			if _, parseErr := contract.ParseTargetActiveState(string(active)); parseErr != nil {
				return contract.TargetComparison{}, ErrInvalidState
			}
			comparison.ActiveState = &active
		}
	default:
		return contract.TargetComparison{}, fmt.Errorf("compare grant request descriptor: %w", err)
	}
	return comparison, nil
}

func decodeAdminEvidence(contents []byte, serverID string, policy contract.Policy, capturedAt string) (contract.DescriptorEvidence, error) {
	compiled, err := CompilePolicy(policy)
	if err != nil {
		return contract.DescriptorEvidence{}, ErrInvalidState
	}
	target, err := normalizePolicyTarget(compiled)
	if err != nil || validateStoredEvidence(contents, serverID, target, capturedAt) != nil {
		return contract.DescriptorEvidence{}, ErrInvalidState
	}
	var evidence contract.DescriptorEvidence
	if err := json.Unmarshal(contents, &evidence); err != nil {
		return contract.DescriptorEvidence{}, ErrInvalidState
	}
	return evidence, nil
}

func cloneRequestState(state *contract.GrantRequestState) *contract.GrantRequestState {
	if state == nil {
		return nil
	}
	value := *state
	return &value
}

func validAdminCursor(cursor AdminCursor, filter AdminFilter) bool {
	return cursor.Collection == adminRequestCollection && cursor.PrincipalID == filter.PrincipalID &&
		equalRequestState(cursor.State, filter.State) && cursor.Upper >= 0 && cursor.After >= 0 && cursor.After <= cursor.Upper &&
		(cursor.After == 0 && cursor.AfterID == "" || cursor.After > 0 && opaqueIDPattern.MatchString(cursor.AfterID))
}

func equalRequestState(left, right *contract.GrantRequestState) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func adminWatermarkQuery(filter AdminFilter) (string, []any) {
	clauses, args := adminFilterClauses(filter)
	return `SELECT COALESCE(max(insertion_sequence), 0) FROM grant_requests` + whereClauses(clauses), args
}

func adminPageQuery(filter AdminFilter, cursor AdminCursor, limit int) (string, []any) {
	clauses, args := adminFilterClauses(filter)
	clauses = append(clauses, `insertion_sequence <= ?`, `(insertion_sequence > ? OR (insertion_sequence = ? AND id > ?))`)
	args = append(args, cursor.Upper, cursor.After, cursor.After, cursor.AfterID, limit)
	return `SELECT insertion_sequence, id, principal_id FROM grant_requests` + whereClauses(clauses) +
		` ORDER BY insertion_sequence, id LIMIT ?`, args
}

func adminFilterClauses(filter AdminFilter) ([]string, []any) {
	var clauses []string
	var args []any
	if filter.PrincipalID != "" {
		clauses, args = append(clauses, `principal_id = ?`), append(args, filter.PrincipalID)
	}
	if filter.State != nil {
		clauses, args = append(clauses, `state = ?`), append(args, *filter.State)
	}
	return clauses, args
}

func whereClauses(clauses []string) string {
	if len(clauses) == 0 {
		return ""
	}
	return ` WHERE ` + strings.Join(clauses, ` AND `)
}
