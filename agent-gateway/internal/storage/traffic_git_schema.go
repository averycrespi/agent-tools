package storage

// TrafficGitMigration adds minimal Git evidence to the selected shared traffic
// generation. No control selector, request data or replay state is introduced.
func TrafficGitMigration() string {
	return `CREATE TABLE git_traffic (
    insertion_sequence INTEGER PRIMARY KEY CHECK (insertion_sequence > 0),
    id TEXT NOT NULL UNIQUE,
    admission TEXT NOT NULL CHECK (json_valid(admission) AND length(CAST(admission AS BLOB)) <= 8192),
    completion TEXT CHECK (completion IS NULL OR (json_valid(completion) AND length(CAST(completion AS BLOB)) <= 512)),
    bytes INTEGER NOT NULL CHECK (bytes > 0)
) STRICT;

CREATE TRIGGER git_traffic_terminal_once BEFORE UPDATE ON git_traffic
WHEN NEW.insertion_sequence IS NOT OLD.insertion_sequence
 OR NEW.id IS NOT OLD.id OR NEW.admission IS NOT OLD.admission
 OR NEW.bytes IS NOT OLD.bytes OR OLD.completion IS NOT NULL
 OR NEW.completion IS NULL
BEGIN
    SELECT RAISE(ABORT, 'immutable Git evidence');
END;

CREATE TRIGGER git_traffic_insert AFTER INSERT ON git_traffic
BEGIN
    UPDATE traffic_meta SET records = records + 1, bytes = bytes + NEW.bytes;
END;

CREATE TRIGGER git_traffic_delete AFTER DELETE ON git_traffic
BEGIN
    UPDATE traffic_meta SET records = records - 1, bytes = bytes - OLD.bytes;
END;
`
}
