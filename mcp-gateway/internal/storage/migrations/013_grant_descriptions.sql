CREATE TABLE grants_replacement (
    insertion_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE CHECK (
        length(id) = 26 AND
        substr(id, 1, 1) BETWEEN '0' AND '7' AND
        id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    principal_id TEXT NOT NULL REFERENCES principals(id),
    effect TEXT NOT NULL CHECK (effect IN ('allow', 'deny')),
    server_id TEXT NOT NULL CHECK (
        length(server_id) = 26 AND
        substr(server_id, 1, 1) BETWEEN '0' AND '7' AND
        server_id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    upstream_name TEXT CHECK (
        upstream_name IS NULL OR
        length(CAST(upstream_name AS BLOB)) BETWEEN 1 AND 128
    ),
    constraint_json TEXT CHECK (
        constraint_json IS NULL OR (
            json_valid(constraint_json) AND
            json_type(constraint_json) = 'object' AND
            length(CAST(constraint_json AS BLOB)) BETWEEN 2 AND 8192
        )
    ),
    expires_at TEXT,
    created_at TEXT NOT NULL,
    description TEXT CHECK (
        description IS NULL OR
        length(CAST(description AS BLOB)) BETWEEN 1 AND 256
    ),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    CHECK (upstream_name IS NOT NULL OR constraint_json IS NULL)
) STRICT;

INSERT INTO grants_replacement (
    insertion_sequence, id, principal_id, effect, server_id, upstream_name,
    constraint_json, expires_at, created_at, description, revision
)
SELECT insertion_sequence, id, principal_id, effect, server_id, upstream_name,
       constraint_json, expires_at, created_at, name, 1
FROM grants;

DROP TABLE grants;
ALTER TABLE grants_replacement RENAME TO grants;

CREATE INDEX grants_principal_insertion
ON grants (principal_id, insertion_sequence, id);

CREATE INDEX grants_server_insertion
ON grants (server_id, insertion_sequence, id);

INSERT INTO schema_migrations (version, name)
VALUES (13, 'grant_descriptions');
