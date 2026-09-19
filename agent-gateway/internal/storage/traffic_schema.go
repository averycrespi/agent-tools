package storage

import "strings"

// TrafficSchema is the unselected traffic generation's DDL. Invocation remains
// the evidence/SQL owner; storage owns schema definitions. This is not a control
// migration and must not be applied to gateway.db.
func TrafficSchema() string {
	migration, err := migrationFiles.ReadFile("migrations/009_invocations.sql")
	if err != nil {
		panic(err)
	}
	ddl, _, ok := strings.Cut(string(migration), "INSERT INTO schema_migrations")
	if !ok {
		panic("invocation schema boundary missing")
	}
	return ddl + `CREATE TABLE traffic_meta (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    installation TEXT NOT NULL,
    generation TEXT NOT NULL,
    records INTEGER NOT NULL CHECK (records >= 0),
    bytes INTEGER NOT NULL CHECK (bytes >= 0),
    high_water INTEGER NOT NULL CHECK (high_water >= 0),
    pruning INTEGER NOT NULL CHECK (pruning >= 0)
) STRICT;

CREATE TABLE traffic_sizes (
    id TEXT PRIMARY KEY REFERENCES invocations(id) ON DELETE CASCADE,
    bytes INTEGER NOT NULL CHECK (bytes > 0)
) STRICT;

CREATE TRIGGER traffic_size_insert AFTER INSERT ON traffic_sizes
BEGIN
    UPDATE traffic_meta SET records = records + 1, bytes = bytes + NEW.bytes;
END;

CREATE TRIGGER traffic_size_delete AFTER DELETE ON traffic_sizes
BEGIN
    UPDATE traffic_meta SET records = records - 1, bytes = bytes - OLD.bytes;
END;
`
}
