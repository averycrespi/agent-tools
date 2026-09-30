ALTER TABLE keyring_authority_fences RENAME TO keyring_authority_fences_previous;

CREATE TABLE keyring_authority_fences (
    owner TEXT NOT NULL CHECK (
        length(owner) = 26 AND
        substr(owner, 1, 1) BETWEEN '0' AND '7' AND
        owner NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    kind TEXT NOT NULL CHECK (kind IN ('static_credential', 'oauth_client', 'oauth_tokens', 'http_credential', 'http_ca', 'git_credential')),
    PRIMARY KEY (owner, kind)
) STRICT;

INSERT INTO keyring_authority_fences SELECT owner, kind FROM keyring_authority_fences_previous;
DROP TABLE keyring_authority_fences_previous;

CREATE TABLE git_credentials (
    insertion_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE CHECK (length(id) = 26 AND substr(id, 1, 1) BETWEEN '0' AND '7' AND id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'),
    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 256),
    origin TEXT NOT NULL CHECK (length(CAST(origin AS BLOB)) BETWEEN 1 AND 4096),
    header TEXT NOT NULL CHECK (length(CAST(header AS BLOB)) BETWEEN 1 AND 128),
    prefix TEXT NOT NULL CHECK (length(CAST(prefix AS BLOB)) <= 128),
    revision INTEGER NOT NULL CHECK (revision > 0),
    material_revision INTEGER NOT NULL CHECK (material_revision >= 0),
    handle TEXT,
    deleted INTEGER NOT NULL CHECK (deleted IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (handle IS NULL OR material_revision > 0),
    CHECK (deleted = 0 OR handle IS NULL)
) STRICT;

CREATE TABLE git_repositories (
    insertion_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE CHECK (length(id) = 26 AND substr(id, 1, 1) BETWEEN '0' AND '7' AND id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'),
    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 256),
    url TEXT NOT NULL CHECK (length(CAST(url AS BLOB)) BETWEEN 1 AND 4096),
    aliases_json TEXT NOT NULL CHECK (length(CAST(aliases_json AS BLOB)) BETWEEN 2 AND 32768 AND json_valid(aliases_json)),
    credential_id TEXT REFERENCES git_credentials(id),
    revision INTEGER NOT NULL CHECK (revision > 0),
    alias_revision INTEGER NOT NULL CHECK (alias_revision > 0),
    deleted INTEGER NOT NULL CHECK (deleted IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE git_grants (
    insertion_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE CHECK (length(id) = 26 AND substr(id, 1, 1) BETWEEN '0' AND '7' AND id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'),
    principal_id TEXT NOT NULL REFERENCES principals(id),
    repository_id TEXT NOT NULL REFERENCES git_repositories(id),
    description TEXT CHECK (description IS NULL OR length(CAST(description AS BLOB)) BETWEEN 1 AND 256),
    revision INTEGER NOT NULL CHECK (revision > 0),
    policy_json TEXT NOT NULL CHECK (length(CAST(policy_json AS BLOB)) BETWEEN 1 AND 32768 AND json_valid(policy_json)),
    expires_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX git_grants_principal ON git_grants(principal_id);

CREATE TABLE git_routing_profile (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    origins_json TEXT NOT NULL CHECK (length(CAST(origins_json AS BLOB)) BETWEEN 2 AND 1048576 AND json_valid(origins_json)),
    revision INTEGER NOT NULL CHECK (revision > 0)
) STRICT;

INSERT INTO git_routing_profile VALUES (1, '[]', 1);

INSERT INTO schema_migrations (version, name) VALUES (22, 'git_authority');
