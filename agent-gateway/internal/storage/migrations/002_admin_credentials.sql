CREATE TABLE admin_credentials (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 26 AND
        id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    verifier BLOB NOT NULL UNIQUE CHECK (length(verifier) = 32),
    fingerprint TEXT NOT NULL UNIQUE CHECK (
        length(fingerprint) = 16 AND
        fingerprint NOT GLOB '*[^0-9a-f]*'
    ),
    created_at TEXT NOT NULL,
    expires_at TEXT,
    status TEXT NOT NULL CHECK (status IN ('active', 'revoked')),
    revision INTEGER NOT NULL CHECK (revision > 0)
) STRICT;

CREATE INDEX admin_credentials_created_id
ON admin_credentials (created_at, id);

INSERT INTO schema_migrations (version, name)
VALUES (2, 'admin_credentials');
