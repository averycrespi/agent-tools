package store

import (
	"database/sql"
	"fmt"
)

// migrations is an ordered slice of migration functions. Each function is called
// exactly once; PRAGMA user_version tracks how many have been applied. Migration
// indices are 1-based (version 1 = migrations[0] applied).
var migrations = []func(*sql.Tx) error{
	// Migration 1: placeholder — no schema changes yet.
	func(_ *sql.Tx) error { return nil },
}

// runMigrations reads the current user_version, then runs each pending migration
// in its own transaction, bumping user_version inside the same transaction.
func runMigrations(db *sql.DB) error {
	current, err := UserVersion(db)
	if err != nil {
		return err
	}

	for i := current; i < len(migrations); i++ {
		if err := runMigration(db, i+1, migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}

	return nil
}

func runMigration(db *sql.DB, version int, fn func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return err
	}

	// PRAGMA user_version cannot be set inside a transaction via a bound
	// parameter, so we use Sprintf with a trusted integer value.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return fmt.Errorf("bump user_version: %w", err)
	}

	return tx.Commit()
}
