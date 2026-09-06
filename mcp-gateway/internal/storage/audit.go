package storage

import (
	"context"
	"fmt"
	"strings"
)

func (store *Store) verifyControlAuditStructure(ctx context.Context) error {
	contents, err := migrationFiles.ReadFile("migrations/015_control_audit.sql")
	if err != nil {
		return fmt.Errorf("read control audit schema: %w", err)
	}
	// Blank-line statement boundaries keep trigger bodies intact. The same
	// embedded DDL defines both migration and subsequent structural acceptance.
	for _, statement := range strings.Split(string(contents), ";\n\n") {
		statement = strings.TrimSpace(statement)
		fields := strings.Fields(statement)
		if len(fields) >= 3 && fields[0] == "ALTER" {
			_, column, ok := strings.Cut(statement, " ADD COLUMN ")
			if !ok {
				return fmt.Errorf("invalid control audit column definition")
			}
			var actual string
			if err := store.database.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?`, fields[2]).Scan(&actual); err != nil {
				return err
			}
			if !strings.Contains(normalizeSchemaSQL(actual), normalizeSchemaSQL(strings.TrimSuffix(column, ";"))) {
				return fmt.Errorf("control audit cause column does not match: %s", fields[2])
			}
			continue
		}
		if len(fields) < 3 || fields[0] != "CREATE" {
			continue
		}
		kind, name := fields[1], fields[2]
		if kind == "UNIQUE" {
			kind, name = fields[2], fields[3]
		}
		var actual string
		if err := store.database.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = ? AND name = ?`, strings.ToLower(kind), name).Scan(&actual); err != nil {
			return fmt.Errorf("read control audit schema object: %w", err)
		}
		if normalizeSchemaSQL(strings.TrimSuffix(actual, ";")) != normalizeSchemaSQL(strings.TrimSuffix(statement, ";")) {
			return fmt.Errorf("control audit schema object does not match: %s", name)
		}
	}
	return nil
}
