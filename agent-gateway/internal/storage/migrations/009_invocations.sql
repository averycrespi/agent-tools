CREATE TABLE invocations (
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
    credential_id TEXT NOT NULL CHECK (
        length(credential_id) = 26 AND
        substr(credential_id, 1, 1) BETWEEN '0' AND '7' AND
        credential_id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    credential_fingerprint TEXT NOT NULL CHECK (
        length(credential_fingerprint) = 16 AND
        credential_fingerprint NOT GLOB '*[^0-9a-f]*'
    ),
    credential_revision INTEGER NOT NULL CHECK (credential_revision > 0),
    admitted_at TEXT NOT NULL CHECK (
        length(CAST(admitted_at AS BLOB)) BETWEEN 1 AND 64
    ),
    admission_class TEXT NOT NULL CHECK (admission_class IN (
        'invalid_params', 'unknown_tool', 'invalid_arguments',
        'authorization_unavailable', 'evaluated'
    )),
    requested_name TEXT CHECK (
        requested_name IS NULL OR
        length(CAST(requested_name AS BLOB)) BETWEEN 1 AND 128
    ),
    redacted_arguments TEXT CHECK (
        redacted_arguments IS NULL OR (
            json_valid(redacted_arguments) AND
            length(CAST(redacted_arguments AS BLOB)) <= 8192 AND
            (json_type(redacted_arguments) = 'object' OR redacted_arguments = '"[TRUNCATED]"')
        )
    ),
    server_id TEXT CHECK (
        server_id IS NULL OR (
            length(server_id) = 26 AND
            substr(server_id, 1, 1) BETWEEN '0' AND '7' AND
            server_id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
        )
    ),
    tool_id TEXT CHECK (
        tool_id IS NULL OR (
            length(tool_id) = 26 AND
            substr(tool_id, 1, 1) BETWEEN '0' AND '7' AND
            tool_id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
        )
    ),
    upstream_name TEXT CHECK (
        upstream_name IS NULL OR
        length(CAST(upstream_name AS BLOB)) BETWEEN 1 AND 128
    ),
    descriptor_revision INTEGER CHECK (
        descriptor_revision IS NULL OR descriptor_revision > 0
    ),
    descriptor_fingerprint TEXT CHECK (
        descriptor_fingerprint IS NULL OR (
            length(descriptor_fingerprint) = 64 AND
            descriptor_fingerprint NOT GLOB '*[^0-9a-f]*'
        )
    ),
    decision TEXT CHECK (decision IS NULL OR decision IN ('allow', 'deny', 'block')),
    authorization_revision INTEGER CHECK (
        authorization_revision IS NULL OR authorization_revision >= 0
    ),
    evaluated_at TEXT CHECK (
        evaluated_at IS NULL OR
        length(CAST(evaluated_at AS BLOB)) BETWEEN 1 AND 64
    ),
    grant_id TEXT CHECK (
        grant_id IS NULL OR (
            length(grant_id) = 26 AND
            substr(grant_id, 1, 1) BETWEEN '0' AND '7' AND
            grant_id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
        )
    ),
    completed_at TEXT CHECK (
        completed_at IS NULL OR
        length(CAST(completed_at AS BLOB)) BETWEEN 1 AND 64
    ),
    terminal_class TEXT CHECK (
        terminal_class IS NULL OR terminal_class IN (
            'prestart_failure', 'succeeded', 'downstream_failure', 'outcome_unknown'
        )
    ),
    CHECK (
        admission_class = 'invalid_params' OR
        (requested_name IS NOT NULL AND redacted_arguments IS NOT NULL)
    ),
    CHECK (
        (server_id IS NULL AND tool_id IS NULL AND upstream_name IS NULL AND
         descriptor_revision IS NULL AND descriptor_fingerprint IS NULL) OR
        (server_id IS NOT NULL AND tool_id IS NOT NULL AND upstream_name IS NOT NULL AND
         descriptor_revision IS NOT NULL AND descriptor_fingerprint IS NOT NULL)
    ),
    CHECK (
        (admission_class IN ('invalid_params', 'unknown_tool') AND server_id IS NULL) OR
        (admission_class IN ('invalid_arguments', 'authorization_unavailable', 'evaluated') AND server_id IS NOT NULL)
    ),
    CHECK (
        (admission_class = 'evaluated' AND decision IS NOT NULL AND
         authorization_revision IS NOT NULL AND evaluated_at IS NOT NULL) OR
        (admission_class <> 'evaluated' AND decision IS NULL AND
         authorization_revision IS NULL AND evaluated_at IS NULL AND grant_id IS NULL)
    ),
    CHECK (
        (completed_at IS NULL AND terminal_class IS NULL) OR
        (completed_at IS NOT NULL AND terminal_class IS NOT NULL AND
         admission_class = 'evaluated' AND decision = 'allow')
    )
) STRICT;

CREATE UNIQUE INDEX invocations_id
ON invocations (id);

CREATE TRIGGER invocations_terminal_once
BEFORE UPDATE ON invocations
WHEN NOT (
    OLD.completed_at IS NULL AND OLD.terminal_class IS NULL AND
    NEW.completed_at IS NOT NULL AND NEW.terminal_class IS NOT NULL AND
    NEW.insertion_sequence IS OLD.insertion_sequence AND
    NEW.id IS OLD.id AND
    NEW.principal_id IS OLD.principal_id AND
    NEW.credential_id IS OLD.credential_id AND
    NEW.credential_fingerprint IS OLD.credential_fingerprint AND
    NEW.credential_revision IS OLD.credential_revision AND
    NEW.admitted_at IS OLD.admitted_at AND
    NEW.admission_class IS OLD.admission_class AND
    NEW.requested_name IS OLD.requested_name AND
    NEW.redacted_arguments IS OLD.redacted_arguments AND
    NEW.server_id IS OLD.server_id AND
    NEW.tool_id IS OLD.tool_id AND
    NEW.upstream_name IS OLD.upstream_name AND
    NEW.descriptor_revision IS OLD.descriptor_revision AND
    NEW.descriptor_fingerprint IS OLD.descriptor_fingerprint AND
    NEW.decision IS OLD.decision AND
    NEW.authorization_revision IS OLD.authorization_revision AND
    NEW.evaluated_at IS OLD.evaluated_at AND
    NEW.grant_id IS OLD.grant_id
)
BEGIN
    SELECT RAISE(ABORT, 'invocation admission is immutable');
END;

INSERT INTO schema_migrations (version, name)
VALUES (9, 'invocations');
