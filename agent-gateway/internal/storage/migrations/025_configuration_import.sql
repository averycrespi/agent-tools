CREATE TABLE configuration_import (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    input_digest TEXT NOT NULL CHECK (length(input_digest) = 64 AND input_digest NOT GLOB '*[^0-9a-f]*'),
    staged_digest TEXT NOT NULL CHECK (length(staged_digest) = 64 AND staged_digest NOT GLOB '*[^0-9a-f]*')
) STRICT;

CREATE TABLE server_import_activation (
    server_id TEXT PRIMARY KEY REFERENCES servers(id)
) STRICT;

CREATE TABLE configuration_import_grant_expiries (
    kind TEXT NOT NULL CHECK (kind IN ('mcp', 'http', 'git')),
    id TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    PRIMARY KEY (kind, id)
) STRICT;

INSERT INTO schema_migrations (version, name) VALUES (25, 'configuration_import');
