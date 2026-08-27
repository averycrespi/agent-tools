CREATE TABLE grant_requests (
    insertion_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL CHECK (
        length(id) = 26 AND
        substr(id, 1, 1) BETWEEN '0' AND '7' AND
        id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    principal_id TEXT NOT NULL CHECK (
        length(principal_id) = 26 AND
        substr(principal_id, 1, 1) BETWEEN '0' AND '7' AND
        principal_id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    state TEXT NOT NULL CHECK (state IN ('pending', 'approved', 'rejected', 'cancelled')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    resolved_server_id TEXT NOT NULL CHECK (
        length(resolved_server_id) = 26 AND
        substr(resolved_server_id, 1, 1) BETWEEN '0' AND '7' AND
        resolved_server_id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    resolved_upstream_name TEXT CHECK (
        resolved_upstream_name IS NULL OR
        length(CAST(resolved_upstream_name AS BLOB)) BETWEEN 1 AND 128
    ),
    requested_scope TEXT NOT NULL CHECK (requested_scope IN ('tool', 'server')),
    requested_target TEXT NOT NULL CHECK (
        length(CAST(requested_target AS BLOB)) BETWEEN 1 AND 128
    ),
    requested_constraint TEXT CHECK (
        requested_constraint IS NULL OR (
            json_valid(requested_constraint) AND
            json_type(requested_constraint) = 'object' AND
            length(CAST(requested_constraint AS BLOB)) <= 8192
        )
    ),
    requested_duration_seconds TEXT CHECK (
        requested_duration_seconds IS NULL OR (
            length(requested_duration_seconds) BETWEEN 2 AND 7 AND
            requested_duration_seconds NOT GLOB '*[^0-9]*' AND
            substr(requested_duration_seconds, 1, 1) <> '0' AND
            CAST(requested_duration_seconds AS INTEGER) BETWEEN 60 AND 2592000 AND
            CAST(CAST(requested_duration_seconds AS INTEGER) AS TEXT) = requested_duration_seconds
        )
    ),
    requested_future_tools_acknowledged INTEGER NOT NULL CHECK (
        requested_future_tools_acknowledged IN (0, 1)
    ),
    dedupe_version INTEGER NOT NULL CHECK (dedupe_version > 0),
    dedupe_bytes BLOB NOT NULL CHECK (
        typeof(dedupe_bytes) = 'blob' AND length(dedupe_bytes) BETWEEN 1 AND 16384
    ),
    submitted_evidence BLOB CHECK (
        submitted_evidence IS NULL OR (
            typeof(submitted_evidence) = 'blob' AND
            length(submitted_evidence) BETWEEN 1 AND 135168
        )
    ),
    approved_scope TEXT CHECK (approved_scope IS NULL OR approved_scope IN ('tool', 'server')),
    approved_target TEXT CHECK (
        approved_target IS NULL OR
        length(CAST(approved_target AS BLOB)) BETWEEN 1 AND 128
    ),
    approved_constraint TEXT CHECK (
        approved_constraint IS NULL OR (
            json_valid(approved_constraint) AND
            json_type(approved_constraint) = 'object' AND
            length(CAST(approved_constraint AS BLOB)) <= 8192
        )
    ),
    approved_duration_seconds TEXT CHECK (
        approved_duration_seconds IS NULL OR (
            length(approved_duration_seconds) BETWEEN 2 AND 7 AND
            approved_duration_seconds NOT GLOB '*[^0-9]*' AND
            substr(approved_duration_seconds, 1, 1) <> '0' AND
            CAST(approved_duration_seconds AS INTEGER) BETWEEN 60 AND 2592000 AND
            CAST(CAST(approved_duration_seconds AS INTEGER) AS TEXT) = approved_duration_seconds
        )
    ),
    approved_future_tools_acknowledged INTEGER CHECK (
        approved_future_tools_acknowledged IS NULL OR
        approved_future_tools_acknowledged IN (0, 1)
    ),
    approved_grant_id TEXT CHECK (
        approved_grant_id IS NULL OR (
            length(approved_grant_id) = 26 AND
            substr(approved_grant_id, 1, 1) BETWEEN '0' AND '7' AND
            approved_grant_id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
        )
    ),
    rejection_reason TEXT CHECK (
        rejection_reason IS NULL OR rejection_reason IN (
            'not_approved', 'existing_access', 'scope_too_broad', 'policy_conflict'
        )
    ),
    approved_evidence BLOB CHECK (
        approved_evidence IS NULL OR (
            typeof(approved_evidence) = 'blob' AND
            length(approved_evidence) BETWEEN 1 AND 135168
        )
    ),
    created_at TEXT NOT NULL CHECK (length(CAST(created_at AS BLOB)) BETWEEN 1 AND 64),
    updated_at TEXT NOT NULL CHECK (length(CAST(updated_at AS BLOB)) BETWEEN 1 AND 64),
    closed_at TEXT CHECK (
        closed_at IS NULL OR length(CAST(closed_at AS BLOB)) BETWEEN 1 AND 64
    ),
    CHECK (
        (requested_scope = 'tool' AND resolved_upstream_name IS NOT NULL AND
         submitted_evidence IS NOT NULL AND requested_future_tools_acknowledged = 0) OR
        (requested_scope = 'server' AND resolved_upstream_name IS NULL AND
         submitted_evidence IS NULL AND requested_constraint IS NULL AND
         requested_future_tools_acknowledged = 1)
    ),
    CHECK (
        state <> 'pending' OR (
            revision = 1 AND updated_at = created_at AND closed_at IS NULL AND
            approved_scope IS NULL AND approved_target IS NULL AND
            approved_constraint IS NULL AND approved_duration_seconds IS NULL AND
            approved_future_tools_acknowledged IS NULL AND approved_grant_id IS NULL AND
            rejection_reason IS NULL AND approved_evidence IS NULL
        )
    ),
    CHECK (
        state <> 'approved' OR (
            revision = 2 AND closed_at IS NOT NULL AND updated_at = closed_at AND
            approved_scope IS NOT NULL AND approved_target IS NOT NULL AND
            approved_future_tools_acknowledged IS NOT NULL AND approved_grant_id IS NOT NULL AND
            rejection_reason IS NULL AND
            ((approved_scope = 'server' AND approved_constraint IS NULL AND
              approved_future_tools_acknowledged = 1 AND approved_evidence IS NULL) OR
             (approved_scope = 'tool' AND approved_future_tools_acknowledged = 0 AND
              ((requested_scope = 'server' AND approved_evidence IS NOT NULL) OR
               (requested_scope = 'tool' AND approved_evidence IS NULL))))
        )
    ),
    CHECK (
        state <> 'rejected' OR (
            revision = 2 AND closed_at IS NOT NULL AND updated_at = closed_at AND
            approved_scope IS NULL AND approved_target IS NULL AND
            approved_constraint IS NULL AND approved_duration_seconds IS NULL AND
            approved_future_tools_acknowledged IS NULL AND approved_grant_id IS NULL AND
            rejection_reason IS NOT NULL AND approved_evidence IS NULL
        )
    ),
    CHECK (
        state <> 'cancelled' OR (
            revision = 2 AND closed_at IS NOT NULL AND updated_at = closed_at AND
            approved_scope IS NULL AND approved_target IS NULL AND
            approved_constraint IS NULL AND approved_duration_seconds IS NULL AND
            approved_future_tools_acknowledged IS NULL AND approved_grant_id IS NULL AND
            rejection_reason IS NULL AND approved_evidence IS NULL
        )
    )
) STRICT;

CREATE TABLE grant_request_evidence_bytes (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    total_bytes INTEGER NOT NULL CHECK (total_bytes BETWEEN 0 AND 268435456)
) STRICT, WITHOUT ROWID;

INSERT INTO grant_request_evidence_bytes (singleton, total_bytes)
VALUES (1, 0);

CREATE UNIQUE INDEX grant_requests_id
ON grant_requests (id);

CREATE UNIQUE INDEX grant_requests_pending_dedupe
ON grant_requests (principal_id, dedupe_version, dedupe_bytes)
WHERE state = 'pending';

CREATE INDEX grant_requests_principal_page
ON grant_requests (principal_id, insertion_sequence);

CREATE INDEX grant_requests_admin_page
ON grant_requests (state, insertion_sequence);

CREATE INDEX grant_requests_pending_principal
ON grant_requests (principal_id)
WHERE state = 'pending';

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
    NEW.dedupe_version IS OLD.dedupe_version AND
    NEW.dedupe_bytes IS OLD.dedupe_bytes AND
    NEW.submitted_evidence IS OLD.submitted_evidence AND
    NEW.created_at IS OLD.created_at
)
BEGIN
    SELECT RAISE(ABORT, 'grant request is immutable outside one terminal transition');
END;

CREATE TRIGGER grant_requests_pending_not_deleted
BEFORE DELETE ON grant_requests
WHEN OLD.state = 'pending'
BEGIN
    SELECT RAISE(ABORT, 'pending grant request cannot be deleted');
END;

CREATE TRIGGER grant_requests_evidence_insert
AFTER INSERT ON grant_requests
BEGIN
    UPDATE grant_request_evidence_bytes
    SET total_bytes = total_bytes +
        COALESCE(length(NEW.submitted_evidence), 0) +
        COALESCE(length(NEW.approved_evidence), 0)
    WHERE singleton = 1;
END;

CREATE TRIGGER grant_requests_evidence_update
AFTER UPDATE OF submitted_evidence, approved_evidence ON grant_requests
BEGIN
    UPDATE grant_request_evidence_bytes
    SET total_bytes = total_bytes -
        COALESCE(length(OLD.submitted_evidence), 0) -
        COALESCE(length(OLD.approved_evidence), 0) +
        COALESCE(length(NEW.submitted_evidence), 0) +
        COALESCE(length(NEW.approved_evidence), 0)
    WHERE singleton = 1;
END;

CREATE TRIGGER grant_requests_evidence_delete
AFTER DELETE ON grant_requests
BEGIN
    UPDATE grant_request_evidence_bytes
    SET total_bytes = total_bytes -
        COALESCE(length(OLD.submitted_evidence), 0) -
        COALESCE(length(OLD.approved_evidence), 0)
    WHERE singleton = 1;
END;

INSERT INTO schema_migrations (version, name)
VALUES (10, 'grant_requests');
