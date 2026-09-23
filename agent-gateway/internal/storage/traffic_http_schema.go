package storage

// TrafficHTTPMigration is additive to traffic schema 1. It neither rewrites MCP
// evidence nor changes the selected generation. The shared writer allocates
// sequences and charges both tables against traffic_meta.
func TrafficHTTPMigration() string {
	return `CREATE TABLE http_traffic (
    insertion_sequence INTEGER PRIMARY KEY CHECK (insertion_sequence > 0),
    id TEXT NOT NULL UNIQUE,
    admission TEXT NOT NULL CHECK (json_valid(admission) AND length(CAST(admission AS BLOB)) <= 65536),
    completion TEXT CHECK (completion IS NULL OR (json_valid(completion) AND length(CAST(completion AS BLOB)) <= 512)),
    bytes INTEGER NOT NULL CHECK (bytes > 0),
    principal_id TEXT GENERATED ALWAYS AS (json_extract(admission, '$.principal.id')) STORED,
    destination TEXT GENERATED ALWAYS AS (json_extract(admission, '$.target.host')) STORED,
    traffic_type TEXT GENERATED ALWAYS AS (CASE WHEN json_type(admission, '$.target') = 'null' THEN 'invalid' WHEN json_extract(admission, '$.target.scheme') IS NULL THEN 'connect' ELSE 'request' END) STORED,
    decision TEXT GENERATED ALWAYS AS (CASE WHEN json_type(admission, '$.decision') = 'null' THEN 'invalid' WHEN json_extract(admission, '$.decision.allowed') = 1 THEN 'allow' WHEN json_extract(admission, '$.decision.transport') = 'intercept' THEN 'intercept' ELSE 'block' END) STORED,
    outcome TEXT GENERATED ALWAYS AS (CASE WHEN json_extract(admission, '$.decision.allowed') = 1 THEN COALESCE(json_extract(completion, '$.outcome'), 'outcome_unknown') ELSE 'not_dispatched' END) STORED
) STRICT;

CREATE INDEX http_traffic_principal ON http_traffic(principal_id, insertion_sequence);

CREATE INDEX http_traffic_destination ON http_traffic(destination, insertion_sequence);

CREATE INDEX http_traffic_type ON http_traffic(traffic_type, insertion_sequence);

CREATE INDEX http_traffic_decision ON http_traffic(decision, insertion_sequence);

CREATE INDEX http_traffic_outcome ON http_traffic(outcome, insertion_sequence);

CREATE TRIGGER http_traffic_terminal_once BEFORE UPDATE ON http_traffic
WHEN NEW.insertion_sequence IS NOT OLD.insertion_sequence
 OR NEW.id IS NOT OLD.id OR NEW.admission IS NOT OLD.admission
 OR NEW.bytes IS NOT OLD.bytes OR OLD.completion IS NOT NULL
 OR NEW.completion IS NULL
BEGIN
    SELECT RAISE(ABORT, 'immutable HTTP evidence');
END;

CREATE TRIGGER http_traffic_insert AFTER INSERT ON http_traffic
BEGIN
    UPDATE traffic_meta SET records = records + 1, bytes = bytes + NEW.bytes;
END;

CREATE TRIGGER http_traffic_delete AFTER DELETE ON http_traffic
BEGIN
    UPDATE traffic_meta SET records = records - 1, bytes = bytes - OLD.bytes;
END;
`
}

// TrafficSchemaVersion retains exact released definitions for immutable backup
// verification. Unknown versions have no schema and must fail closed.
func TrafficSchemaVersion(version int) string {
	switch version {
	case 1:
		return TrafficSchema()
	case 2:
		return TrafficSchema() + "\n" + TrafficHTTPMigration()
	default:
		return ""
	}
}
