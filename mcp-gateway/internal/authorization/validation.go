package authorization

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

const canonicalTimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

var opaqueIDPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

type StoredGrantTargetInspector interface {
	StoredGrantTargetExistsTx(context.Context, *sql.Tx, string) (bool, error)
}

func (repository *Repository) ValidateStartup(ctx context.Context, targets StoredGrantTargetInspector) error {
	if targets == nil {
		return errorsInvalidState("grant-target inspector is unavailable")
	}
	return repository.view(ctx, func(transaction *sql.Tx) error {
		return validateAuthorityTx(ctx, transaction, targets)
	})
}

func validateAuthorityTx(ctx context.Context, transaction *sql.Tx, targets StoredGrantTargetInspector) error {
	if err := validateSingletons(ctx, transaction, targets); err != nil {
		return err
	}
	principalIDs, err := validatePrincipals(ctx, transaction)
	if err != nil {
		return err
	}
	return validateGrants(ctx, transaction, targets, principalIDs)
}

func validateSingletons(ctx context.Context, transaction *sql.Tx, targets StoredGrantTargetInspector) error {
	rows, err := transaction.QueryContext(ctx, `SELECT singleton, server_id, namespace FROM synthetic_server_identity`)
	if err != nil {
		return fmt.Errorf("read synthetic identity: %w", err)
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		var singleton int64
		var serverID, namespace string
		if err := rows.Scan(&singleton, &serverID, &namespace); err != nil {
			return fmt.Errorf("scan synthetic identity: %w", err)
		}
		count++
		if singleton != 1 || serverID != contract.SyntheticServerID || namespace != contract.SyntheticServerNamespace {
			return errorsInvalidState("synthetic identity is malformed")
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate synthetic identity: %w", err)
	}
	if count != 1 {
		return errorsInvalidState("synthetic identity singleton count is invalid")
	}
	exists, err := targets.StoredGrantTargetExistsTx(ctx, transaction, contract.SyntheticServerID)
	if err != nil {
		return fmt.Errorf("inspect synthetic target collision: %w", err)
	}
	if !exists {
		return errorsInvalidState("synthetic identity collides with S2")
	}

	rows, err = transaction.QueryContext(ctx, `SELECT singleton, revision FROM authorization_meta`)
	if err != nil {
		return fmt.Errorf("read authorization metadata: %w", err)
	}
	defer func() { _ = rows.Close() }()
	count = 0
	for rows.Next() {
		var singleton, revision int64
		if err := rows.Scan(&singleton, &revision); err != nil {
			return fmt.Errorf("scan authorization metadata: %w", err)
		}
		count++
		if singleton != 1 || revision < 0 {
			return errorsInvalidState("authorization metadata is malformed")
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate authorization metadata: %w", err)
	}
	if count != 1 {
		return errorsInvalidState("authorization metadata singleton count is invalid")
	}
	return nil
}

func validatePrincipals(ctx context.Context, transaction *sql.Tx) (map[string]struct{}, error) {
	maximum := mustLimit("principals")
	rows, err := transaction.QueryContext(ctx, `
		SELECT insertion_sequence, id, display_name, state, visibility, revision,
		       credential_revision, credential_id, credential_verifier,
		       credential_fingerprint, credential_created_at, created_at, updated_at
		FROM principals ORDER BY insertion_sequence, id LIMIT ?`, maximum+1)
	if err != nil {
		return nil, fmt.Errorf("read principals for validation: %w", err)
	}
	defer func() { _ = rows.Close() }()
	ids := make(map[string]struct{})
	var previousSequence int64
	for rows.Next() {
		var (
			sequence                                  int64
			id, displayName, state, visibility        string
			revision, credentialRevision              int64
			credentialID, fingerprint, credentialTime sql.NullString
			verifier                                  []byte
			createdAt, updatedAt                      string
		)
		if err := rows.Scan(&sequence, &id, &displayName, &state, &visibility, &revision, &credentialRevision,
			&credentialID, &verifier, &fingerprint, &credentialTime, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan principal for validation: %w", err)
		}
		if int64(len(ids)) >= maximum {
			return nil, errorsInvalidState("principal capacity is exceeded")
		}
		if sequence <= previousSequence || !validOpaqueID(id) || !validDisplayName(displayName) ||
			(state != string(contract.PrincipalActive) && state != string(contract.PrincipalDisabled)) ||
			(visibility != string(contract.VisibilityRequestable) && visibility != string(contract.VisibilityAllowedOnly) && visibility != string(contract.VisibilityAll)) ||
			revision <= 0 || credentialRevision < 0 {
			return nil, errorsInvalidState("principal row is malformed")
		}
		created, ok := canonicalTimestamp(createdAt)
		if !ok {
			return nil, errorsInvalidState("principal creation timestamp is malformed")
		}
		updated, ok := canonicalTimestamp(updatedAt)
		if !ok || updated.Before(created) {
			return nil, errorsInvalidState("principal update timestamp is malformed")
		}
		credentialPresent := credentialID.Valid || verifier != nil || fingerprint.Valid || credentialTime.Valid
		credentialComplete := credentialID.Valid && len(verifier) == 32 && fingerprint.Valid && credentialTime.Valid
		if credentialPresent != credentialComplete || credentialRevision == 0 && credentialComplete ||
			state == string(contract.PrincipalDisabled) && credentialComplete {
			return nil, errorsInvalidState("principal credential slot is malformed")
		}
		if credentialComplete {
			issued, ok := canonicalTimestamp(credentialTime.String)
			if !validOpaqueID(credentialID.String) || !validFingerprint(fingerprint.String) || !ok || issued.Before(created) || issued.After(updated) {
				return nil, errorsInvalidState("principal credential metadata is malformed")
			}
		}
		previousSequence = sequence
		ids[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate principals for validation: %w", err)
	}
	return ids, nil
}

func validateGrants(ctx context.Context, transaction *sql.Tx, targets StoredGrantTargetInspector, principalIDs map[string]struct{}) error {
	maximum := mustLimit("grants")
	rows, err := transaction.QueryContext(ctx, `
		SELECT insertion_sequence, id, name, principal_id, effect, server_id, upstream_name,
		       constraint_json, expires_at, created_at
		FROM grants ORDER BY insertion_sequence, id LIMIT ?`, maximum+1)
	if err != nil {
		return fmt.Errorf("read grants for validation: %w", err)
	}
	defer func() { _ = rows.Close() }()
	validatedTargets := map[string]bool{contract.SyntheticServerID: true}
	count := int64(0)
	var previousSequence int64
	for rows.Next() {
		var (
			sequence                                int64
			id, name, principalID, effect, serverID string
			upstreamName, constraintJSON, expiresAt sql.NullString
			createdAt                               string
		)
		if err := rows.Scan(&sequence, &id, &name, &principalID, &effect, &serverID, &upstreamName, &constraintJSON, &expiresAt, &createdAt); err != nil {
			return fmt.Errorf("scan grant for validation: %w", err)
		}
		if count >= maximum {
			return errorsInvalidState("grant capacity is exceeded")
		}
		count++
		if sequence <= previousSequence || !validOpaqueID(id) || !validGrantName(name) || !validOpaqueID(principalID) || !validOpaqueID(serverID) ||
			(effect != string(contract.GrantAllow) && effect != string(contract.GrantDeny)) {
			return errorsInvalidState("grant row is malformed")
		}
		if _, exists := principalIDs[principalID]; !exists {
			return errorsInvalidState("grant principal is missing")
		}
		if upstreamName.Valid && !validUpstreamName(upstreamName.String) {
			return errorsInvalidState("grant upstream name is malformed")
		}
		if !upstreamName.Valid && constraintJSON.Valid {
			return errorsInvalidState("server-wide grant has a constraint")
		}
		if constraintJSON.Valid {
			if _, err := CompileConstraint([]byte(constraintJSON.String)); err != nil {
				return errorsInvalidState("grant constraint is malformed")
			}
		}
		created, ok := canonicalTimestamp(createdAt)
		if !ok {
			return errorsInvalidState("grant creation timestamp is malformed")
		}
		if expiresAt.Valid {
			expiry, ok := canonicalTimestamp(expiresAt.String)
			if !ok || !expiry.After(created) {
				return errorsInvalidState("grant expiry timestamp is malformed")
			}
		}
		targetExists, inspected := validatedTargets[serverID]
		if !inspected {
			targetExists, err = targets.StoredGrantTargetExistsTx(ctx, transaction, serverID)
			if err != nil {
				return fmt.Errorf("inspect stored grant target: %w", err)
			}
			validatedTargets[serverID] = targetExists
		}
		if !targetExists {
			return errorsInvalidState("grant target is missing")
		}
		previousSequence = sequence
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate grants for validation: %w", err)
	}
	return nil
}

func validOpaqueID(value string) bool { return opaqueIDPattern.MatchString(value) }

func validDisplayName(value string) bool {
	if !utf8.ValidString(value) || len(value) < 1 || int64(len(value)) > mustLimit("display_name_bytes") {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validGrantName(value string) bool {
	if !utf8.ValidString(value) || len(value) < 1 || int64(len(value)) > mustLimit("grant_name_bytes") || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validUpstreamName(value string) bool {
	if len(value) < 1 || int64(len(value)) > mustLimit("tool_name_bytes") {
		return false
	}
	for _, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("_-.", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func validFingerprint(value string) bool {
	if len(value) != 16 {
		return false
	}
	for _, character := range []byte(value) {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func canonicalTimestamp(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	parsed = parsed.UTC()
	return parsed, parsed.Format(canonicalTimestampLayout) == value
}

func errorsInvalidState(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidState, reason)
}
