package authorization

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

const principalSelect = `
	SELECT insertion_sequence, id, display_name, state, visibility, revision,
	       credential_revision, credential_id, credential_fingerprint,
	       credential_created_at, created_at, updated_at
	FROM principals`

type principalScanner interface {
	Scan(dest ...any) error
}

func (repository *Repository) GetPrincipal(ctx context.Context, principalID string) (contract.Principal, error) {
	var principal contract.Principal
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		var scanErr error
		principal, scanErr = principalByIDTx(ctx, transaction, principalID)
		return scanErr
	})
	return principal, err
}

func principalByIDTx(ctx context.Context, transaction *sql.Tx, principalID string) (contract.Principal, error) {
	_, principal, err := scanPrincipal(transaction.QueryRowContext(ctx, principalSelect+` WHERE id = ?`, principalID))
	return principal, err
}

func (repository *Repository) ListPrincipals(ctx context.Context, cursor *SnapshotCursor, limit int) (PrincipalPage, error) {
	if err := validatePageLimit(limit); err != nil {
		return PrincipalPage{}, err
	}
	var page PrincipalPage
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		snapshot := SnapshotCursor{Collection: principalCollection, Expires: repository.clock.Now().Add(contract.AuthorizationCursorLifetime).Unix()}
		if cursor == nil {
			if err := transaction.QueryRowContext(ctx, `SELECT coalesce(max(insertion_sequence), 0) FROM principals`).Scan(&snapshot.Upper); err != nil {
				return fmt.Errorf("capture principal insertion watermark: %w", err)
			}
		} else {
			snapshot = *cursor
			if !repository.validCursor(snapshot, principalCollection, GrantFilter{}) {
				return ErrStaleCursor
			}
		}
		rows, err := transaction.QueryContext(ctx, principalSelect+`
			WHERE (insertion_sequence > ? OR (insertion_sequence = ? AND id > ?))
			  AND insertion_sequence <= ?
			ORDER BY insertion_sequence, id LIMIT ?`, snapshot.After, snapshot.After, snapshot.AfterID, snapshot.Upper, limit+1)
		if err != nil {
			return fmt.Errorf("list principals: %w", err)
		}
		defer func() { _ = rows.Close() }()
		items := make([]contract.Principal, 0, limit+1)
		sequences := make([]int64, 0, limit+1)
		for rows.Next() {
			sequence, principal, scanErr := scanPrincipal(rows)
			if scanErr != nil {
				return scanErr
			}
			sequences = append(sequences, sequence)
			items = append(items, principal)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate principals: %w", err)
		}
		if len(items) > limit {
			next := snapshot
			next.After = sequences[limit-1]
			next.AfterID = items[limit-1].ID
			repository.sealCursor(&next)
			page.Next = &next
			items = items[:limit]
		}
		page.Items = items
		return nil
	})
	return page, err
}

func scanPrincipal(scanner principalScanner) (int64, contract.Principal, error) {
	var (
		sequence              int64
		principal             contract.Principal
		revision              int64
		credentialRevision    int64
		credentialID          sql.NullString
		credentialFingerprint sql.NullString
		credentialCreatedAt   sql.NullString
	)
	if err := scanner.Scan(
		&sequence, &principal.ID, &principal.DisplayName, &principal.State, &principal.Visibility,
		&revision, &credentialRevision, &credentialID, &credentialFingerprint,
		&credentialCreatedAt, &principal.CreatedAt, &principal.UpdatedAt,
	); err != nil {
		return 0, contract.Principal{}, err
	}
	principal.Revision = strconv.FormatInt(revision, 10)
	principal.CredentialRevision = strconv.FormatInt(credentialRevision, 10)
	credentialPresent := credentialID.Valid || credentialFingerprint.Valid || credentialCreatedAt.Valid
	credentialComplete := credentialID.Valid && credentialFingerprint.Valid && credentialCreatedAt.Valid
	if credentialPresent != credentialComplete {
		return 0, contract.Principal{}, fmt.Errorf("principal current credential slot is incomplete")
	}
	if credentialComplete {
		principal.Credential = &contract.AgentCredential{
			ID: credentialID.String, Fingerprint: credentialFingerprint.String,
			Revision: principal.CredentialRevision, CreatedAt: credentialCreatedAt.String,
		}
	}
	return sequence, principal, nil
}

func (repository *Repository) validCursor(cursor SnapshotCursor, collection string, filter GrantFilter) bool {
	positionValid := cursor.After >= 0 && cursor.Upper >= 0 && cursor.After <= cursor.Upper &&
		((cursor.After == 0 && cursor.AfterID == "") || (cursor.After > 0 && cursor.AfterID != ""))
	return positionValid && cursor.Query == "" && cursor.Collection == collection && cursor.PrincipalID == filter.PrincipalID && cursor.ServerID == filter.ServerID && repository.authenticCursor(cursor)
}
