ALTER TABLE grants ADD COLUMN read_only INTEGER NOT NULL DEFAULT 0
    CHECK (read_only IN (0, 1) AND
        (read_only = 0 OR (effect = 'allow' AND upstream_name IS NULL AND constraint_json IS NULL)));

ALTER TABLE grant_requests ADD COLUMN requested_read_only INTEGER NOT NULL DEFAULT 0
    CHECK (requested_read_only IN (0, 1) AND
        (requested_read_only = 0 OR (requested_scope = 'server' AND requested_constraint IS NULL)));

ALTER TABLE grant_requests ADD COLUMN approved_read_only INTEGER NOT NULL DEFAULT 0
    CHECK (approved_read_only IN (0, 1) AND
        (approved_read_only = 0 OR (state = 'approved' AND approved_scope = 'server' AND approved_constraint IS NULL)) AND
        (requested_read_only = 0 OR state <> 'approved' OR approved_read_only = 1));

CREATE TRIGGER grants_read_only_immutable
BEFORE UPDATE OF read_only ON grants
WHEN NEW.read_only IS NOT OLD.read_only
BEGIN
    SELECT RAISE(ABORT, 'grant policy is immutable');
END;

DROP TRIGGER grant_requests_terminal_once;

CREATE TRIGGER grant_requests_terminal_once
BEFORE UPDATE ON grant_requests
WHEN NOT (
    OLD.state = 'pending' AND NEW.state IN ('approved', 'rejected', 'cancelled') AND
    NEW.revision = OLD.revision + 1 AND
    NEW.updated_at IS NEW.closed_at AND NEW.closed_at IS NOT NULL AND
    NEW.insertion_sequence IS OLD.insertion_sequence AND
    NEW.id IS OLD.id AND
    NEW.principal_id IS OLD.principal_id AND
    NEW.resolved_server_id IS OLD.resolved_server_id AND
    NEW.resolved_upstream_name IS OLD.resolved_upstream_name AND
    NEW.requested_scope IS OLD.requested_scope AND
    NEW.requested_target IS OLD.requested_target AND
    NEW.requested_constraint IS OLD.requested_constraint AND
    NEW.requested_duration_seconds IS OLD.requested_duration_seconds AND
    NEW.requested_future_tools_acknowledged IS OLD.requested_future_tools_acknowledged AND
    NEW.requested_read_only IS OLD.requested_read_only AND
    NEW.dedupe_version IS OLD.dedupe_version AND
    NEW.dedupe_bytes IS OLD.dedupe_bytes AND
    NEW.submitted_evidence IS OLD.submitted_evidence AND
    NEW.created_at IS OLD.created_at
)
BEGIN
    SELECT RAISE(ABORT, 'grant request is immutable outside one terminal transition');
END;

INSERT INTO schema_migrations (version, name) VALUES (16, 'read_only_grants');
