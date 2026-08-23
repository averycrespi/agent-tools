package storage

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"

	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
)

// BackupTo copies one consistent committed database snapshot through SQLite's online backup API.
func (store *Store) BackupTo(ctx context.Context, destination string) error {
	file, err := gatewaypaths.CreateOwnerOnlyFile(destination)
	if err != nil {
		return fmt.Errorf("create backup database: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("close backup database: %w", err)
	}
	connection, err := store.database.Conn(ctx)
	if err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("acquire backup source connection: %w", err)
	}
	defer func() { _ = connection.Close() }()
	uri := &url.URL{Scheme: "file", Path: destination, RawQuery: "mode=rw"}
	if err := connection.Raw(func(raw any) error {
		return raw.(sqliteDriver.Conn).Raw().Backup("main", uri.String())
	}); err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("copy online backup: %w", err)
	}
	if err := gatewaypaths.ValidateOwnerOnlyFile(destination); err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("validate backup database: %w", err)
	}
	return nil
}

// VerifyBackup validates a closed current or accepted S1 schema-3 backup without changing it.
func VerifyBackup(ctx context.Context, path string) (Identity, error) {
	if err := gatewaypaths.ValidateOwnerOnlyFile(path); err != nil {
		return Identity{}, fmt.Errorf("%w: backup path: %w", ErrInvalidDatabase, err)
	}
	database, err := sql.Open("sqlite3", dataSource(path, true, false))
	if err != nil {
		return Identity{}, fmt.Errorf("%w: open backup: %w", ErrInvalidDatabase, err)
	}
	defer func() { _ = database.Close() }()
	var applicationID, schema int
	var installationID string
	var revision int64
	var integrity string
	if err := database.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&applicationID); err != nil {
		return Identity{}, fmt.Errorf("%w: read backup application ID: %w", ErrInvalidDatabase, err)
	}
	if err := database.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil {
		return Identity{}, fmt.Errorf("%w: read backup schema: %w", ErrInvalidDatabase, err)
	}
	if err := database.QueryRowContext(ctx, `SELECT installation_id, revision FROM gateway_meta WHERE singleton = 1`).Scan(&installationID, &revision); err != nil {
		return Identity{}, fmt.Errorf("%w: read backup identity: %w", ErrInvalidDatabase, err)
	}
	if err := database.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return Identity{}, fmt.Errorf("%w: check backup integrity: %w", ErrInvalidDatabase, err)
	}
	if applicationID != ApplicationID || schema < 3 || schema > CurrentSchema || !installationIDPattern.MatchString(installationID) || revision < 0 || integrity != "ok" {
		return Identity{}, fmt.Errorf("%w: backup identity or integrity mismatch", ErrInvalidDatabase)
	}
	return Identity{InstallationID: installationID, SchemaVersion: schema, Revision: uint64(revision)}, nil
}
