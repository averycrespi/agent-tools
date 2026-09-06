CREATE TABLE control_audit_history (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    generation TEXT NOT NULL CHECK (length(generation) = 64 AND generation NOT GLOB '*[^0-9a-f]*'),
    pruned INTEGER NOT NULL CHECK (pruned IN (0, 1))
) STRICT, WITHOUT ROWID;

INSERT INTO control_audit_history VALUES (1, lower(hex(randomblob(32))), 0);

CREATE TABLE control_audit_events (
    insertion_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    event TEXT NOT NULL CHECK (length(CAST(event AS BLOB)) BETWEEN 1 AND 2048 AND json_valid(event)),
    id TEXT GENERATED ALWAYS AS (json_extract(event, '$.id')) STORED NOT NULL,
    timestamp TEXT GENERATED ALWAYS AS (json_extract(event, '$.timestamp')) STORED NOT NULL,
    category TEXT GENERATED ALWAYS AS (json_extract(event, '$.category')) STORED NOT NULL,
    action TEXT GENERATED ALWAYS AS (json_extract(event, '$.action')) STORED NOT NULL,
    actor_type TEXT GENERATED ALWAYS AS (json_extract(event, '$.actor.type')) STORED NOT NULL,
    credential_id TEXT GENERATED ALWAYS AS (json_extract(event, '$.actor.credential.id')) STORED,
    initiator_id TEXT GENERATED ALWAYS AS (json_extract(event, '$.initiator.id')) STORED,
    target_type TEXT GENERATED ALWAYS AS (json_extract(event, '$.target.type')) STORED NOT NULL,
    target_id TEXT GENERATED ALWAYS AS (json_extract(event, '$.target.id')) STORED NOT NULL,
    outcome TEXT GENERATED ALWAYS AS (json_extract(event, '$.outcome')) STORED NOT NULL,
    correlation_id TEXT GENERATED ALWAYS AS (json_extract(event, '$.correlation_id')) STORED NOT NULL,
    CHECK (CAST(insertion_sequence AS TEXT) = json_extract(event, '$.sequence')),
    CHECK (actor_type IN ('operator', 'system', 'offline_maintenance')),
    CHECK (outcome IN ('pending', 'succeeded', 'rejected', 'failed', 'unknown'))
) STRICT;

CREATE UNIQUE INDEX control_audit_id ON control_audit_events (id);

CREATE INDEX control_audit_actor ON control_audit_events (actor_type, insertion_sequence);

CREATE INDEX control_audit_credential ON control_audit_events (credential_id, insertion_sequence);

CREATE INDEX control_audit_initiator ON control_audit_events (initiator_id, insertion_sequence);

CREATE INDEX control_audit_action ON control_audit_events (category, action, insertion_sequence);

CREATE INDEX control_audit_target ON control_audit_events (target_type, target_id, insertion_sequence);

CREATE INDEX control_audit_outcome ON control_audit_events (outcome, insertion_sequence);

CREATE INDEX control_audit_correlation ON control_audit_events (correlation_id, insertion_sequence);

CREATE INDEX control_audit_time ON control_audit_events (timestamp, insertion_sequence);

CREATE TRIGGER control_audit_ordered
BEFORE INSERT ON control_audit_events
WHEN NEW.insertion_sequence <> COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'control_audit_events'), 0) + 1
    OR NEW.timestamp < COALESCE((SELECT timestamp FROM control_audit_events ORDER BY insertion_sequence DESC LIMIT 1), '')
BEGIN
    SELECT RAISE(ABORT, 'control audit sequence is not chronological');
END;

CREATE TRIGGER control_audit_prune_only
BEFORE DELETE ON control_audit_events
WHEN OLD.insertion_sequence > (SELECT max(insertion_sequence) FROM control_audit_events) - 65536
BEGIN
    SELECT RAISE(ABORT, 'retained control audit events cannot be deleted');
END;

CREATE TRIGGER control_audit_immutable
BEFORE UPDATE ON control_audit_events
BEGIN
    SELECT RAISE(ABORT, 'control audit events are immutable');
END;

CREATE TRIGGER control_audit_retain
AFTER INSERT ON control_audit_events
BEGIN
    UPDATE control_audit_history SET pruned = 1
    WHERE singleton = 1 AND EXISTS (
        SELECT 1 FROM control_audit_events WHERE insertion_sequence <= NEW.insertion_sequence - 65536
    );
    DELETE FROM control_audit_events WHERE insertion_sequence <= NEW.insertion_sequence - 65536;
END;

ALTER TABLE servers ADD COLUMN audit_cause TEXT
    CHECK (audit_cause IS NULL OR (length(CAST(audit_cause AS BLOB)) BETWEEN 1 AND 2048 AND json_valid(audit_cause)));

CREATE TRIGGER server_audit_cause_revision
BEFORE UPDATE OF audit_cause ON servers
WHEN NEW.desired_revision = OLD.desired_revision AND NEW.audit_cause IS NOT OLD.audit_cause
BEGIN
    SELECT RAISE(ABORT, 'server audit cause requires a desired revision');
END;

ALTER TABLE server_operations ADD COLUMN audit_cause TEXT
    CHECK (audit_cause IS NULL OR (length(CAST(audit_cause AS BLOB)) BETWEEN 1 AND 2048 AND json_valid(audit_cause)));

ALTER TABLE server_auth_flows ADD COLUMN audit_cause TEXT
    CHECK (audit_cause IS NULL OR (length(CAST(audit_cause AS BLOB)) BETWEEN 1 AND 2048 AND json_valid(audit_cause)));

CREATE TRIGGER server_operation_audit_cause_immutable
BEFORE UPDATE OF audit_cause ON server_operations
BEGIN
    SELECT RAISE(ABORT, 'operation audit cause is immutable');
END;

CREATE TRIGGER server_auth_flow_audit_cause_immutable
BEFORE UPDATE OF audit_cause ON server_auth_flows
BEGIN
    SELECT RAISE(ABORT, 'auth flow audit cause is immutable');
END;

INSERT INTO schema_migrations (version, name) VALUES (15, 'control_audit');
