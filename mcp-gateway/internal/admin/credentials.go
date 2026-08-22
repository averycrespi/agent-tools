package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

var (
	ErrInvalidExpiry   = errors.New("credential expiry is outside the allowed range")
	ErrResourceLimit   = errors.New("admin credential limit is reached")
	ErrNotFound        = errors.New("admin credential was not found")
	ErrLastNonExpiring = errors.New("the last active non-expiring authority cannot be revoked")
	ErrMutationBusy    = storage.ErrMutationBusy
)

type credentialRecord struct {
	metadata contract.AdminCredential
	verifier []byte
	expires  *time.Time
}

func (service *Service) Create(ctx context.Context, expiresAt *time.Time) (contract.CreatedAdminCredential, error) {
	service.secretOps.Lock()
	defer service.secretOps.Unlock()

	now := service.clock.Now()
	if expiresAt != nil {
		lifetime := expiresAt.Sub(now)
		if lifetime < contract.CredentialMinimumLifetime || lifetime > contract.CredentialMaximumLifetime {
			return contract.CreatedAdminCredential{}, ErrInvalidExpiry
		}
	}
	candidate, err := service.prepareCredential(expiresAt)
	if err != nil {
		return contract.CreatedAdminCredential{}, err
	}
	if err := service.store.Mutate(ctx, func(transaction *sql.Tx) error {
		if err := pruneForInsert(ctx, transaction, now); err != nil {
			return err
		}
		revision, err := incrementRevision(ctx, transaction)
		if err != nil {
			return err
		}
		candidate.metadata.Revision = revision
		return insertCredential(ctx, transaction, candidate)
	}); err != nil {
		return contract.CreatedAdminCredential{}, err
	}
	return contract.CreatedAdminCredential{AdminCredential: candidate.metadata, Bearer: candidate.bearer}, nil
}

func (service *Service) Get(ctx context.Context, id string) (contract.AdminCredential, error) {
	var metadata contract.AdminCredential
	err := service.store.View(ctx, func(transaction *sql.Tx) error {
		record, err := queryCredential(ctx, transaction, id, service.clock.Now())
		if err != nil {
			return err
		}
		metadata = record.metadata
		return nil
	})
	return metadata, err
}

func (service *Service) List(ctx context.Context) ([]contract.AdminCredential, error) {
	items := make([]contract.AdminCredential, 0)
	err := service.store.View(ctx, func(transaction *sql.Tx) error {
		rows, err := transaction.QueryContext(ctx, `
			SELECT id, verifier, fingerprint, created_at, expires_at, status, revision
			FROM admin_credentials ORDER BY id`)
		if err != nil {
			return fmt.Errorf("list admin credentials: %w", err)
		}
		defer func() { _ = rows.Close() }()
		now := service.clock.Now()
		for rows.Next() {
			record, err := scanCredential(rows, now)
			if err != nil {
				return err
			}
			items = append(items, record.metadata)
		}
		return rows.Err()
	})
	return items, err
}

func (service *Service) Revoke(ctx context.Context, id string) error {
	revoked := false
	err := service.store.Mutate(ctx, func(transaction *sql.Tx) error {
		record, err := queryCredential(ctx, transaction, id, service.clock.Now())
		if err != nil {
			return err
		}
		if record.metadata.Status != contract.CredentialActive {
			return nil
		}
		if record.metadata.NonExpiring {
			var activeNonExpiring int
			if err := transaction.QueryRowContext(ctx, `
				SELECT count(*) FROM admin_credentials
				WHERE status = 'active' AND expires_at IS NULL`).Scan(&activeNonExpiring); err != nil {
				return fmt.Errorf("count active non-expiring credentials: %w", err)
			}
			if activeNonExpiring <= 1 {
				return ErrLastNonExpiring
			}
		}
		revision, err := incrementRevision(ctx, transaction)
		if err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE admin_credentials SET status = 'revoked', revision = ? WHERE id = ?`, revision, id); err != nil {
			return fmt.Errorf("revoke admin credential: %w", err)
		}
		revoked = true
		return nil
	})
	if err != nil {
		return err
	}
	if revoked {
		service.notifyCredentialInvalidation(&id)
	}
	return nil
}

func queryCredential(ctx context.Context, transaction *sql.Tx, id string, now time.Time) (credentialRecord, error) {
	row := transaction.QueryRowContext(ctx, `
		SELECT id, verifier, fingerprint, created_at, expires_at, status, revision
		FROM admin_credentials WHERE id = ?`, id)
	record, err := scanCredential(row, now)
	if errors.Is(err, sql.ErrNoRows) {
		return credentialRecord{}, ErrNotFound
	}
	return record, err
}

type credentialScanner interface {
	Scan(...any) error
}

func scanCredential(scanner credentialScanner, now time.Time) (credentialRecord, error) {
	var record credentialRecord
	var createdAt string
	var expiresAt sql.NullString
	var persistedStatus string
	var revision uint64
	if err := scanner.Scan(
		&record.metadata.ID,
		&record.verifier,
		&record.metadata.Fingerprint,
		&createdAt,
		&expiresAt,
		&persistedStatus,
		&revision,
	); err != nil {
		return credentialRecord{}, err
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return credentialRecord{}, fmt.Errorf("parse credential creation time: %w", err)
	}
	record.metadata.CreatedAt = created.UTC().Format(time.RFC3339Nano)
	record.metadata.NonExpiring = !expiresAt.Valid
	record.metadata.Revision = fmt.Sprintf("%d", revision)
	record.metadata.Status = contract.CredentialStatus(persistedStatus)
	if expiresAt.Valid {
		expires, err := time.Parse(time.RFC3339Nano, expiresAt.String)
		if err != nil {
			return credentialRecord{}, fmt.Errorf("parse credential expiry: %w", err)
		}
		expires = expires.UTC()
		record.expires = &expires
		formatted := expires.Format(time.RFC3339Nano)
		record.metadata.ExpiresAt = &formatted
		if record.metadata.Status == contract.CredentialActive && !now.Before(expires) {
			record.metadata.Status = contract.CredentialExpired
		}
	}
	return record, nil
}

type terminalCredential struct {
	id        string
	createdAt time.Time
}

func pruneForInsert(ctx context.Context, transaction *sql.Tx, now time.Time) error {
	limit, ok := contract.FixedLimitByName("admin_credentials")
	if !ok {
		return fmt.Errorf("admin credential limit is missing")
	}
	rows, err := transaction.QueryContext(ctx, `
		SELECT id, created_at, expires_at, status FROM admin_credentials`)
	if err != nil {
		return fmt.Errorf("inspect admin credential capacity: %w", err)
	}
	terminals := make([]terminalCredential, 0)
	count := int64(0)
	for rows.Next() {
		count++
		var id, createdAt, status string
		var expiresAt sql.NullString
		if err := rows.Scan(&id, &createdAt, &expiresAt, &status); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan admin credential capacity: %w", err)
		}
		created, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("parse admin credential creation time: %w", err)
		}
		terminal := status == string(contract.CredentialRevoked)
		if expiresAt.Valid {
			expires, err := time.Parse(time.RFC3339Nano, expiresAt.String)
			if err != nil {
				_ = rows.Close()
				return fmt.Errorf("parse admin credential expiry: %w", err)
			}
			terminal = terminal || !now.Before(expires)
		}
		if terminal {
			terminals = append(terminals, terminalCredential{id: id, createdAt: created})
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close admin credential capacity rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate admin credential capacity: %w", err)
	}
	needed := count - limit.Maximum + 1
	if needed <= 0 {
		return nil
	}
	if int64(len(terminals)) < needed {
		return ErrResourceLimit
	}
	sort.Slice(terminals, func(left, right int) bool {
		if terminals[left].createdAt.Equal(terminals[right].createdAt) {
			return terminals[left].id < terminals[right].id
		}
		return terminals[left].createdAt.Before(terminals[right].createdAt)
	})
	for _, terminal := range terminals[:needed] {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM admin_credentials WHERE id = ?`, terminal.id); err != nil {
			return fmt.Errorf("prune terminal admin credential: %w", err)
		}
	}
	return nil
}
