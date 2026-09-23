ALTER TABLE keyring_authority_fences RENAME TO keyring_authority_fences_previous;

CREATE TABLE keyring_authority_fences (
    owner TEXT NOT NULL CHECK (
        length(owner) = 26 AND
        substr(owner, 1, 1) BETWEEN '0' AND '7' AND
        owner NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    kind TEXT NOT NULL CHECK (kind IN ('static_credential', 'oauth_client', 'oauth_tokens', 'http_credential')),
    PRIMARY KEY (owner, kind)
) STRICT;

INSERT INTO keyring_authority_fences SELECT owner, kind FROM keyring_authority_fences_previous;
DROP TABLE keyring_authority_fences_previous;

CREATE TABLE http_credentials (
    insertion_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE CHECK (
        length(id) = 26 AND substr(id, 1, 1) BETWEEN '0' AND '7' AND id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 256),
    host TEXT NOT NULL CHECK (length(CAST(host AS BLOB)) BETWEEN 1 AND 255),
    port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    allow_wildcard INTEGER NOT NULL CHECK (allow_wildcard IN (0, 1)),
    header TEXT NOT NULL CHECK (length(CAST(header AS BLOB)) BETWEEN 1 AND 128),
    prefix TEXT NOT NULL CHECK (length(CAST(prefix AS BLOB)) <= 128),
    revision INTEGER NOT NULL CHECK (revision > 0),
    material_revision INTEGER NOT NULL CHECK (material_revision >= 0),
    handle TEXT,
    deleted INTEGER NOT NULL CHECK (deleted IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (deleted = 0 OR handle IS NULL),
    CHECK (handle IS NULL OR material_revision > 0)
) STRICT;

INSERT INTO schema_migrations (version, name) VALUES (19, 'http_credentials');
