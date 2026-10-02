package gitcredentials

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

func ValidateStartup(ctx context.Context, store *storage.Store) error {
	return store.View(ctx, func(tx *sql.Tx) error { return validateTx(ctx, tx) })
}
func validateTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM git_credentials ORDER BY id LIMIT ?`, identityLimit+1)
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
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
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
		canonical, err := Normalize(rec.GitCredentialDefinition)
		if err != nil || canonical != rec.GitCredentialDefinition || !contract.ValidAuditID(id) || !gitRevision(rec.Revision) {
			return ErrInvalid
		}
		revision, _ := strconv.ParseUint(rec.Revision, 10, 63)
		if rec.materialRevision > revision {
			return ErrInvalid
		}
		created, err := time.Parse(contract.AuditTimestampLayout, rec.CreatedAt)
		if err != nil || created.Format(contract.AuditTimestampLayout) != rec.CreatedAt {
			return ErrInvalid
		}
		updated, err := time.Parse(contract.AuditTimestampLayout, rec.UpdatedAt)
		if err != nil || updated.Format(contract.AuditTimestampLayout) != rec.UpdatedAt || updated.Before(created) {
			return ErrInvalid
		}
		if rec.handle.Valid {
			if _, err := keyring.ParseHandle(rec.handle.String); err != nil || rec.materialRevision == 0 || rec.deleted {
				return ErrInvalid
			}
			var valid int
			if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM keyring_authority_fences WHERE owner=? AND kind='git_credential') + (SELECT count(*) FROM keyring_authorities WHERE owner=? AND kind='git_credential' AND handle=? AND revision=?)`, id, id, rec.handle.String, rec.materialRevision).Scan(&valid); err != nil {
				return err
			}
			if valid == 0 {
				return ErrInvalid
			}
		}
		if !rec.deleted {
			active++
		}
	}
	if active > contract.GitCredentials {
		return ErrInvalid
	}
	var invalid int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM keyring_authorities a LEFT JOIN git_credentials c ON c.id=a.owner WHERE a.kind='git_credential' AND (c.id IS NULL OR c.deleted<>0 OR c.handle IS NULL OR c.handle<>a.handle OR c.material_revision<>a.revision)`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return ErrInvalid
	}
	for _, query := range []string{
		`SELECT count(*) FROM keyring_candidates a LEFT JOIN git_credentials c ON c.id=a.owner WHERE a.kind='git_credential' AND c.id IS NULL`,
		`SELECT count(*) FROM keyring_authority_fences a LEFT JOIN git_credentials c ON c.id=a.owner WHERE a.kind='git_credential' AND c.id IS NULL`,
	} {
		if err := tx.QueryRowContext(ctx, query).Scan(&invalid); err != nil {
			return err
		}
		if invalid != 0 {
			return ErrInvalid
		}
	}
	limit, ok := contract.FixedLimitByName("keyring_candidates")
	if !ok {
		return ErrInvalid
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT owner FROM keyring_candidates WHERE kind='git_credential' GROUP BY owner HAVING count(*)>?)`, limit.Maximum).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return ErrInvalid
	}
	rows, err = tx.QueryContext(ctx, `SELECT handle FROM keyring_candidates WHERE kind='git_credential' UNION ALL SELECT handle FROM keyring_authorities WHERE kind='git_credential' LIMIT ?`, identityLimit*(limit.Maximum+1)+1)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	for rows.Next() {
		var handle string
		if err := rows.Scan(&handle); err != nil {
			return err
		}
		if _, err := keyring.ParseHandle(handle); err != nil || seen[handle] {
			return ErrInvalid
		}
		seen[handle] = true
		if int64(len(seen)) > identityLimit*(limit.Maximum+1) {
			return ErrInvalid
		}
	}
	return rows.Err()
}
func gitRevision(raw string) bool {
	n, err := strconv.ParseInt(raw, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == raw
}

// VerifyBackup validates the copied closed artifact, never the live source or
// native keyring. Historical schemas without Git authority remain supported.
func VerifyBackup(ctx context.Context, path string, schema int) error {
	if schema < 22 {
		return nil
	}
	return storage.ViewBackup(ctx, path, func(tx *sql.Tx) error {
		if err := validateTx(ctx, tx); err != nil {
			return err
		}
		return authorization.ValidateGitBackupTx(ctx, tx)
	})
}

// InvalidateStagedCredentials acts only on a stopped replacement. Retained
// configuration survives; every selected Git handle loses authority, even when
// old physical provider chunks remain. No provider operation is performed.
func InvalidateStagedCredentials(ctx context.Context, store *storage.Store, clock Clock) error {
	return store.Mutate(ctx, func(tx *sql.Tx) error {
		if err := validateTx(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM git_credentials WHERE handle IS NOT NULL ORDER BY id LIMIT ?`, identityLimit+1)
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
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if len(ids) > identityLimit {
			return ErrInvalid
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `DELETE FROM keyring_authorities WHERE owner=? AND kind='git_credential'`, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO keyring_authority_fences(owner,kind) VALUES(?,'git_credential')`, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE git_credentials SET handle=NULL,revision=revision+1,material_revision=material_revision+1,updated_at=? WHERE id=?`, clock.Now().UTC().Format(contract.AuditTimestampLayout), id); err != nil {
				return err
			}
			if err := authorization.AdvanceGitCredentialRevisionTx(ctx, tx); err != nil {
				return err
			}
			if err := audit.MutationTx(ctx, tx, clock.Now(), "git_credential", "invalidate", contract.AuditTarget{Type: "git_credential", ID: id}); err != nil {
				return err
			}
		}
		return validateTx(ctx, tx)
	})
}
