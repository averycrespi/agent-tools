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

	// Migration 2: secrets table (AES-256-GCM encrypted values, per-row nonce).
	func(tx *sql.Tx) error {
		_, err := tx.Exec(`
CREATE TABLE secrets (
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL,
  scope        TEXT NOT NULL,
  ciphertext   BLOB NOT NULL,
  nonce        BLOB NOT NULL,
  created_at   INTEGER NOT NULL,
  rotated_at   INTEGER NOT NULL,
  last_used_at INTEGER,
  description  TEXT,
  UNIQUE(name, scope)
);
CREATE INDEX idx_secrets_scope ON secrets(scope);
`)
		return err
	},

	// Migration 3: agents table (argon2id token auth, per-row salt).
	func(tx *sql.Tx) error {
		_, err := tx.Exec(`
CREATE TABLE agents (
  name         TEXT PRIMARY KEY,
  token_hash   BLOB NOT NULL,
  token_prefix TEXT NOT NULL,
  argon2_salt  BLOB NOT NULL,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER,
  description  TEXT
);
`)
		return err
	},

	// Migration 4: requests audit table with four covering indexes.
	func(tx *sql.Tx) error {
		_, err := tx.Exec(`
CREATE TABLE requests (
  id               TEXT PRIMARY KEY,
  ts               INTEGER NOT NULL,
  agent            TEXT REFERENCES agents(name) ON DELETE SET NULL,
  interception     TEXT NOT NULL,
  method           TEXT,
  host             TEXT NOT NULL,
  path             TEXT,
  query            TEXT,
  status           INTEGER,
  duration_ms      INTEGER NOT NULL,
  bytes_in         INTEGER NOT NULL,
  bytes_out        INTEGER NOT NULL,
  matched_rule     TEXT,
  rule_verdict     TEXT,
  approval         TEXT,
  injection        TEXT,
  outcome          TEXT NOT NULL,
  credential_ref   TEXT,
  credential_scope TEXT,
  error            TEXT
);
CREATE INDEX idx_req_ts    ON requests(ts);
CREATE INDEX idx_req_agent ON requests(ts, agent);
CREATE INDEX idx_req_host  ON requests(ts, host);
CREATE INDEX idx_req_rule  ON requests(matched_rule, ts);
`)
		return err
	},

	// Migration 5: drop the FK on requests.agent so that audit rows can
	// reference agents that have been deleted (or never existed). SQLite does
	// not support DROP CONSTRAINT, so we recreate the table without the FK.
	// All data and indexes are preserved.
	func(tx *sql.Tx) error {
		_, err := tx.Exec(`
CREATE TABLE requests_new (
  id               TEXT PRIMARY KEY,
  ts               INTEGER NOT NULL,
  agent            TEXT,
  interception     TEXT NOT NULL,
  method           TEXT,
  host             TEXT NOT NULL,
  path             TEXT,
  query            TEXT,
  status           INTEGER,
  duration_ms      INTEGER NOT NULL,
  bytes_in         INTEGER NOT NULL,
  bytes_out        INTEGER NOT NULL,
  matched_rule     TEXT,
  rule_verdict     TEXT,
  approval         TEXT,
  injection        TEXT,
  outcome          TEXT NOT NULL,
  credential_ref   TEXT,
  credential_scope TEXT,
  error            TEXT
);
INSERT INTO requests_new SELECT * FROM requests;
DROP TABLE requests;
ALTER TABLE requests_new RENAME TO requests;
CREATE INDEX idx_req_ts    ON requests(ts);
CREATE INDEX idx_req_agent ON requests(ts, agent);
CREATE INDEX idx_req_host  ON requests(ts, host);
CREATE INDEX idx_req_rule  ON requests(matched_rule, ts);
`)
		return err
	},
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
