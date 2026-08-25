CREATE TABLE synthetic_server_identity (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    server_id TEXT NOT NULL UNIQUE CHECK (
        server_id = '00000000000000000000000000'
    ),
    namespace TEXT NOT NULL UNIQUE CHECK (namespace = 'mcp_gateway')
) STRICT;

CREATE TABLE authorization_meta (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    revision INTEGER NOT NULL CHECK (revision >= 0)
) STRICT;

CREATE TABLE principals (
    insertion_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE CHECK (
        length(id) = 26 AND
        substr(id, 1, 1) BETWEEN '0' AND '7' AND
        id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    display_name TEXT NOT NULL CHECK (
        length(CAST(display_name AS BLOB)) BETWEEN 1 AND 256
    ),
    state TEXT NOT NULL CHECK (state IN ('active', 'disabled')),
    visibility TEXT NOT NULL CHECK (visibility IN ('requestable', 'allowed-only', 'all')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    credential_revision INTEGER NOT NULL CHECK (credential_revision >= 0),
    credential_id TEXT UNIQUE CHECK (
        credential_id IS NULL OR (
            length(credential_id) = 26 AND
            substr(credential_id, 1, 1) BETWEEN '0' AND '7' AND
            credential_id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
        )
    ),
    credential_verifier BLOB UNIQUE CHECK (
        credential_verifier IS NULL OR length(credential_verifier) = 32
    ),
    credential_fingerprint TEXT UNIQUE CHECK (
        credential_fingerprint IS NULL OR (
            length(credential_fingerprint) = 16 AND
            credential_fingerprint NOT GLOB '*[^0-9a-f]*'
        )
    ),
    credential_created_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (
        (credential_id IS NULL AND credential_verifier IS NULL AND
         credential_fingerprint IS NULL AND credential_created_at IS NULL) OR
        (credential_id IS NOT NULL AND credential_verifier IS NOT NULL AND
         credential_fingerprint IS NOT NULL AND credential_created_at IS NOT NULL)
    ),
    CHECK (credential_revision > 0 OR credential_id IS NULL)
) STRICT;

CREATE TABLE grants (
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
    CHECK (upstream_name IS NOT NULL OR constraint_json IS NULL)
) STRICT;

CREATE INDEX grants_principal_insertion
ON grants (principal_id, insertion_sequence, id);

CREATE INDEX grants_server_insertion
ON grants (server_id, insertion_sequence, id);

INSERT INTO synthetic_server_identity (singleton, server_id, namespace)
VALUES (1, '00000000000000000000000000', 'mcp_gateway');

INSERT INTO authorization_meta (singleton, revision)
VALUES (1, 0);

INSERT INTO schema_migrations (version, name)
VALUES (8, 'authorization');
