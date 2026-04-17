package store

import "database/sql"

// SetMigrationsForTest replaces the package-level migrations slice and returns
// the previous slice so callers can restore it with defer.
func SetMigrationsForTest(m []func(*sql.Tx) error) []func(*sql.Tx) error {
	old := migrations
	migrations = m
	return old
}
