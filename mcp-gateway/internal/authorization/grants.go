package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

const grantSelect = `
	SELECT insertion_sequence, id, principal_id, effect, server_id, upstream_name,
	       constraint_json, expires_at, created_at
	FROM grants`

type grantScanner interface {
	Scan(dest ...any) error
}

func (repository *Repository) GetGrant(ctx context.Context, grantID string) (contract.Grant, error) {
	var grant contract.Grant
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		var scanErr error
		_, grant, scanErr = scanGrant(transaction.QueryRowContext(ctx, grantSelect+` WHERE id = ?`, grantID), repository.clock.Now())
		return scanErr
	})
	return grant, err
}

func (repository *Repository) ListGrants(ctx context.Context, filter GrantFilter, cursor *SnapshotCursor, limit int) (GrantPage, error) {
	if err := validatePageLimit(limit); err != nil {
		return GrantPage{}, err
	}
	var page GrantPage
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		snapshot := SnapshotCursor{Collection: grantCollection, PrincipalID: filter.PrincipalID, ServerID: filter.ServerID}
		if cursor == nil {
			if err := transaction.QueryRowContext(ctx, `SELECT coalesce(max(insertion_sequence), 0) FROM grants`).Scan(&snapshot.Upper); err != nil {
				return fmt.Errorf("capture grant insertion watermark: %w", err)
			}
		} else {
			snapshot = *cursor
			if !validCursor(snapshot, grantCollection, filter) {
				return ErrStaleCursor
			}
		}
		now := repository.clock.Now()
		rows, err := transaction.QueryContext(ctx, grantSelect+`
			WHERE (insertion_sequence > ? OR (insertion_sequence = ? AND id > ?))
			  AND insertion_sequence <= ?
			  AND (? = '' OR principal_id = ?)
			  AND (? = '' OR server_id = ?)
			ORDER BY insertion_sequence, id LIMIT ?`,
			snapshot.After, snapshot.After, snapshot.AfterID, snapshot.Upper,
			filter.PrincipalID, filter.PrincipalID, filter.ServerID, filter.ServerID, limit+1)
		if err != nil {
			return fmt.Errorf("list grants: %w", err)
		}
		defer func() { _ = rows.Close() }()
		items := make([]contract.Grant, 0, limit+1)
		sequences := make([]int64, 0, limit+1)
		for rows.Next() {
			sequence, grant, scanErr := scanGrant(rows, now)
			if scanErr != nil {
				return scanErr
			}
			sequences = append(sequences, sequence)
			items = append(items, grant)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate grants: %w", err)
		}
		if len(items) > limit {
			next := snapshot
			next.After = sequences[limit-1]
			next.AfterID = items[limit-1].ID
			page.Next = &next
			items = items[:limit]
		}
		page.Items = items
		return nil
	})
	return page, err
}

func scanGrant(scanner grantScanner, now time.Time) (int64, contract.Grant, error) {
	var (
		sequence       int64
		grant          contract.Grant
		upstreamName   sql.NullString
		constraintJSON sql.NullString
		expiresAt      sql.NullString
	)
	if err := scanner.Scan(
		&sequence, &grant.ID, &grant.PrincipalID, &grant.Effect, &grant.ServerID,
		&upstreamName, &constraintJSON, &expiresAt, &grant.CreatedAt,
	); err != nil {
		return 0, contract.Grant{}, err
	}
	if upstreamName.Valid {
		value := upstreamName.String
		grant.UpstreamName = &value
	}
	if constraintJSON.Valid {
		if _, err := CompileConstraint([]byte(constraintJSON.String)); err != nil {
			return 0, contract.Grant{}, errorsInvalidState("grant constraint is malformed")
		}
		value := json.RawMessage(append([]byte(nil), constraintJSON.String...))
		grant.Constraint = &value
	}
	grant.State = contract.GrantActive
	if expiresAt.Valid {
		value := expiresAt.String
		grant.ExpiresAt = &value
		expiry, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return 0, contract.Grant{}, fmt.Errorf("parse grant expiry: %w", err)
		}
		if !expiry.After(now) {
			grant.State = contract.GrantExpired
		}
	}
	return sequence, grant, nil
}
