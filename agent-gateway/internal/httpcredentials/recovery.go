package httpcredentials

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// NoHTTPGrants is the schema-19 composition: HTTP grants do not yet have a
// durable owner. Grant management must replace this inspector when it adds one;
// it must not use this projection for a populated grant store.
type NoHTTPGrants struct{}

func (NoHTTPGrants) ReferencesTx(context.Context, *sql.Tx, string) ([]Reference, error) {
	return []Reference{}, nil
}

func ValidateStartup(ctx context.Context, store *storage.Store) error {
	return store.View(ctx, func(tx *sql.Tx) error { return validateTx(ctx, tx) })
}

func validateTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM http_credentials ORDER BY insertion_sequence LIMIT ?`, identityLimit+1)
	if err != nil {
		return err
	}
	ids := []string{}
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
	if len(ids) > identityLimit {
		return ErrInvalid
	}
	active := 0
	for _, id := range ids {
		rec, err := readTx(ctx, tx, id)
		if err != nil {
			return err
		}
		canonical, err := Normalize(rec.Definition)
		if err != nil || canonical != rec.Definition || !contract.ValidAuditID(rec.ID) {
			return ErrInvalid
		}
		revision, err := strconv.ParseUint(rec.Revision, 10, 63)
		if err != nil || revision == 0 || rec.materialRevision > revision {
			return ErrInvalid
		}
		created, err := time.Parse(time.RFC3339Nano, rec.CreatedAt)
		if err != nil {
			return ErrInvalid
		}
		updated, err := time.Parse(time.RFC3339Nano, rec.UpdatedAt)
		if err != nil || updated.Before(created) {
			return ErrInvalid
		}
		if rec.handle.Valid {
			if _, err := keyring.ParseHandle(rec.handle.String); err != nil || rec.materialRevision == 0 || rec.deleted {
				return ErrInvalid
			}
		}
		if !rec.deleted {
			active++
		}
	}
	if active > contract.HTTPPolicyCredentials {
		return ErrInvalid
	}
	var orphan int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM keyring_authorities a LEFT JOIN http_credentials c ON c.id=a.owner WHERE a.kind='http_credential' AND (c.id IS NULL OR c.deleted<>0 OR c.handle IS NULL OR c.handle<>a.handle OR c.material_revision<>a.revision)`).Scan(&orphan); err != nil {
		return err
	}
	if orphan != 0 {
		return ErrInvalid
	}
	return nil
}
