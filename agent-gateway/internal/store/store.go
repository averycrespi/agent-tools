package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// Open opens or creates the SQLite database at path, ensures the parent directory
// exists with 0o700 permissions, enables WAL mode, sets busy_timeout and
// foreign_keys, and runs any pending migrations.
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign_keys: %w", err)
	}

	if err := runMigrations(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	// Force WAL/SHM file creation with a trivial write so the subsequent chmod
	// can tighten all three files. WAL mode alone does not create state.db-wal
	// and state.db-shm on disk until a write actually triggers the write-ahead
	// log; without this probe, the chmod below would silently skip non-existent
	// sidecar files and they'd be created later with process-umask defaults.
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS _chmod_probe(x INTEGER)"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("warm-up write: %w", err)
	}

	// Tighten file modes on the SQLite database and its WAL/SHM sidecars.
	// SQLite creates these files honoring the process umask, which on a typical
	// operator system is 0o022 — leaving state.db, state.db-wal, and
	// state.db-shm world-readable. Those files contain the audit log (host,
	// path, headers), argon2id agent token hashes, and AES-256-GCM ciphertexts
	// of every injected secret. An explicit chmod here is defense-in-depth
	// alongside the process-wide 0o077 umask set in runServe: the umask
	// protects future file creation, this chmod protects files SQLite has
	// already written with wider modes before the umask took effect (e.g. when
	// Open is called from a CLI subcommand that did not tighten umask).
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("chmod %s%s: %w", path, suffix, err)
		}
	}

	return db, nil
}

// UserVersion returns the current PRAGMA user_version value.
func UserVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}
	return v, nil
}
