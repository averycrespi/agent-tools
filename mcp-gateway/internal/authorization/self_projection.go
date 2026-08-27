package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

// StoredGrantNamespaceInspector resolves immutable grant targets without opening another transaction.
type StoredGrantNamespaceInspector interface {
	LookupStoredGrantNamespaceTx(context.Context, *sql.Tx, string) (namespace string, found bool, err error)
}

// SelfProjectionService exposes only admitted-principal identity and ordinary grant reads.
type SelfProjectionService struct {
	repository *Repository
	targets    StoredGrantNamespaceInspector
}

// SelfGrantCursor is an authorization-owned insertion watermark and position.
type SelfGrantCursor struct {
	Upper   int64
	After   int64
	AfterID string
}

// SelfGrantPage is one coherent admitted-principal grant page.
type SelfGrantPage struct {
	Items []contract.AgentGrant
	Next  *SelfGrantCursor
}

// NewSelfProjectionService binds coherent S3 reads to immutable target-name authority.
func NewSelfProjectionService(repository *Repository, targets StoredGrantNamespaceInspector) (*SelfProjectionService, error) {
	if repository == nil || targets == nil {
		return nil, errors.New("self projection dependencies are incomplete")
	}
	return &SelfProjectionService{repository: repository, targets: targets}, nil
}

// ReadSelfIdentity returns the current safe projection for the admitted subject.
func (service *SelfProjectionService) ReadSelfIdentity(ctx context.Context, subject AdmittedSubject) (contract.SelfIdentity, error) {
	if service == nil || service.repository == nil || !subject.validFor(service.repository) {
		return contract.SelfIdentity{}, ErrAuthenticationRequired
	}
	var identity contract.SelfIdentity
	err := service.repository.view(ctx, func(transaction *sql.Tx) error {
		var err error
		identity, err = readSelfIdentityTx(ctx, transaction, subject)
		return err
	})
	return identity, err
}

// ListSelfGrants returns one insertion-watermarked page for the admitted subject.
func (service *SelfProjectionService) ListSelfGrants(
	ctx context.Context,
	subject AdmittedSubject,
	cursor *SelfGrantCursor,
	limit int,
) (SelfGrantPage, error) {
	if service == nil || service.repository == nil || service.targets == nil || !subject.validFor(service.repository) {
		return SelfGrantPage{}, ErrAuthenticationRequired
	}
	if limit < 1 || int64(limit) > mustLimit("agent_self_service_list_page") {
		return SelfGrantPage{}, ErrInvalidInput
	}
	var page SelfGrantPage
	err := service.repository.view(ctx, func(transaction *sql.Tx) error {
		if _, err := readSelfIdentityTx(ctx, transaction, subject); err != nil {
			return err
		}
		position := SelfGrantCursor{}
		if cursor == nil {
			if err := transaction.QueryRowContext(ctx, `SELECT COALESCE(max(insertion_sequence), 0) FROM grants WHERE principal_id = ?`, subject.principalID).Scan(&position.Upper); err != nil {
				return fmt.Errorf("capture self grant insertion watermark: %w", err)
			}
		} else {
			position = *cursor
			if !validSelfGrantCursor(position) {
				return ErrStaleCursor
			}
		}
		now := service.repository.clock.Now().UTC()
		rows, err := transaction.QueryContext(ctx, grantSelect+`
			WHERE principal_id = ?
			  AND (insertion_sequence > ? OR (insertion_sequence = ? AND id > ?))
			  AND insertion_sequence <= ?
			ORDER BY insertion_sequence, id LIMIT ?`,
			subject.principalID, position.After, position.After, position.AfterID, position.Upper, limit+1)
		if err != nil {
			return fmt.Errorf("list self grants: %w", err)
		}
		grants := make([]selfGrantRow, 0, limit+1)
		for rows.Next() {
			grant, scanErr := scanSelfGrant(rows, subject.principalID, now)
			if scanErr != nil {
				_ = rows.Close()
				return scanErr
			}
			grants = append(grants, grant)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate self grants: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close self grants: %w", err)
		}

		projected := make([]contract.AgentGrant, 0, min(len(grants), limit))
		namespaces := make(map[string]string)
		for index, row := range grants {
			if index == limit {
				break
			}
			namespace, exists := namespaces[row.serverID]
			if !exists {
				var found bool
				namespace, found, err = service.targets.LookupStoredGrantNamespaceTx(ctx, transaction, row.serverID)
				if err != nil {
					return fmt.Errorf("inspect self grant target: %w", err)
				}
				if !found || !validProjectedNamespace(namespace, row.serverID == contract.SyntheticServerID) {
					return errorsInvalidState("self grant target is malformed or missing")
				}
				namespaces[row.serverID] = namespace
			}
			target := namespace
			scope := contract.PolicyServer
			if row.upstreamName != nil {
				scope = contract.PolicyTool
				target += "." + *row.upstreamName
			}
			projected = append(projected, contract.AgentGrant{
				ID: row.id, Effect: row.effect,
				Policy:    contract.GrantPolicy{Scope: scope, Target: target, Constraint: row.constraint},
				ExpiresAt: row.expiresAt, State: row.state, CreatedAt: row.createdAt,
			})
		}
		if len(grants) > limit {
			last := grants[limit-1]
			next := position
			next.After, next.AfterID = last.sequence, last.id
			page.Next = &next
		}
		page.Items = projected
		return nil
	})
	if err != nil {
		return SelfGrantPage{}, err
	}
	return page, nil
}

func readSelfIdentityTx(ctx context.Context, transaction *sql.Tx, subject AdmittedSubject) (contract.SelfIdentity, error) {
	var (
		identity                              contract.SelfIdentity
		principalRevision, credentialRevision int64
		credentialID                          sql.NullString
		createdAt, updatedAt                  string
	)
	err := transaction.QueryRowContext(ctx, `SELECT
		id, display_name, state, visibility, revision, credential_revision,
		credential_id, created_at, updated_at
	FROM principals WHERE id = ?`, subject.principalID).Scan(
		&identity.ID, &identity.DisplayName, &identity.State, &identity.Visibility,
		&principalRevision, &credentialRevision, &credentialID, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.SelfIdentity{}, ErrAuthenticationRequired
	}
	if err != nil {
		return contract.SelfIdentity{}, fmt.Errorf("read self identity: %w", err)
	}
	created, createdValid := canonicalTimestamp(createdAt)
	updated, updatedValid := canonicalTimestamp(updatedAt)
	if !validOpaqueID(identity.ID) || !validDisplayName(identity.DisplayName) || !validPrincipalState(identity.State) ||
		!validVisibility(identity.Visibility) || principalRevision < 1 || credentialRevision < 0 ||
		!createdValid || !updatedValid || updated.Before(created) {
		return contract.SelfIdentity{}, ErrAuthorizationUnavailable
	}
	if identity.State != contract.PrincipalActive || !credentialID.Valid {
		return contract.SelfIdentity{}, ErrAuthenticationRequired
	}
	if !validOpaqueID(credentialID.String) {
		return contract.SelfIdentity{}, ErrAuthorizationUnavailable
	}
	identity.PrincipalRevision = strconv.FormatInt(principalRevision, 10)
	identity.CredentialRevision = strconv.FormatInt(credentialRevision, 10)
	if identity.ID != subject.principalID || identity.PrincipalRevision != subject.principalRevision ||
		credentialID.String != subject.credentialID || identity.CredentialRevision != subject.credentialRevision {
		return contract.SelfIdentity{}, ErrAuthenticationRequired
	}
	return identity, nil
}

type selfGrantRow struct {
	sequence     int64
	id           string
	effect       contract.GrantEffect
	serverID     string
	upstreamName *string
	constraint   *json.RawMessage
	expiresAt    *string
	state        contract.GrantState
	createdAt    string
}

func scanSelfGrant(scanner grantScanner, principalID string, now time.Time) (selfGrantRow, error) {
	var (
		row                                     selfGrantRow
		storedPrincipalID                       string
		upstreamName, constraintJSON, expiresAt sql.NullString
	)
	if err := scanner.Scan(&row.sequence, &row.id, &storedPrincipalID, &row.effect, &row.serverID,
		&upstreamName, &constraintJSON, &expiresAt, &row.createdAt); err != nil {
		return selfGrantRow{}, fmt.Errorf("scan self grant: %w", err)
	}
	created, createdValid := canonicalTimestamp(row.createdAt)
	if row.sequence < 1 || !validOpaqueID(row.id) || storedPrincipalID != principalID || !validOpaqueID(row.serverID) ||
		(row.effect != contract.GrantAllow && row.effect != contract.GrantDeny) || !createdValid {
		return selfGrantRow{}, errorsInvalidState("self grant row is malformed")
	}
	if upstreamName.Valid {
		if !validUpstreamName(upstreamName.String) {
			return selfGrantRow{}, errorsInvalidState("self grant target is malformed")
		}
		value := upstreamName.String
		row.upstreamName = &value
	}
	if constraintJSON.Valid {
		if !upstreamName.Valid {
			return selfGrantRow{}, errorsInvalidState("server-wide self grant has a constraint")
		}
		if _, err := CompileConstraint([]byte(constraintJSON.String)); err != nil {
			return selfGrantRow{}, errorsInvalidState("self grant constraint is malformed")
		}
		value := json.RawMessage(append([]byte(nil), constraintJSON.String...))
		row.constraint = &value
	}
	row.state = contract.GrantActive
	if expiresAt.Valid {
		expiry, valid := canonicalTimestamp(expiresAt.String)
		if !valid || !expiry.After(created) {
			return selfGrantRow{}, errorsInvalidState("self grant expiry is malformed")
		}
		value := expiresAt.String
		row.expiresAt = &value
		if !expiry.After(now) {
			row.state = contract.GrantExpired
		}
	}
	return row, nil
}

func validSelfGrantCursor(cursor SelfGrantCursor) bool {
	return cursor.Upper >= 0 && cursor.After >= 0 && cursor.After <= cursor.Upper &&
		(cursor.After == 0 && cursor.AfterID == "" || cursor.After > 0 && validOpaqueID(cursor.AfterID))
}

func validProjectedNamespace(namespace string, synthetic bool) bool {
	if synthetic {
		return namespace == contract.SyntheticServerNamespace
	}
	if namespace == contract.SyntheticServerNamespace || len(namespace) < 1 || len(namespace) > 32 {
		return false
	}
	for index, character := range []byte(namespace) {
		if character >= 'a' && character <= 'z' || index > 0 && character >= '0' && character <= '9' ||
			index > 0 && (character == '_' || character == '-') {
			continue
		}
		return false
	}
	return true
}
