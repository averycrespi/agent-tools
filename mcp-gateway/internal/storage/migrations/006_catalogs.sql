CREATE TABLE server_catalogs (
    server_id TEXT PRIMARY KEY REFERENCES servers(id),
    durable_revision INTEGER NOT NULL CHECK (durable_revision >= 0),
    durable_state TEXT NOT NULL CHECK (durable_state IN ('empty', 'current', 'stale', 'unavailable')),
    durable_tool_count INTEGER NOT NULL CHECK (durable_tool_count >= 0),
    issue_count INTEGER NOT NULL CHECK (issue_count >= 0),
    last_success_at TEXT,
    CHECK ((durable_revision = 0) = (last_success_at IS NULL)),
    CHECK (durable_revision > 0 OR durable_state IN ('empty', 'unavailable'))
) STRICT;

CREATE TABLE durable_tool_identities (
    insertion_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE CHECK (
        length(id) = 26 AND
        substr(id, 1, 1) BETWEEN '0' AND '7' AND
        id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    server_id TEXT NOT NULL REFERENCES servers(id),
    upstream_name TEXT NOT NULL CHECK (length(CAST(upstream_name AS BLOB)) BETWEEN 1 AND 128),
    external_name TEXT NOT NULL CHECK (length(CAST(external_name AS BLOB)) BETWEEN 3 AND 128),
    first_seen_at TEXT NOT NULL,
    UNIQUE (server_id, upstream_name)
) STRICT;

CREATE INDEX durable_tool_identities_server_insertion
ON durable_tool_identities (server_id, insertion_sequence, id);

CREATE TABLE tool_descriptors (
    tool_id TEXT PRIMARY KEY REFERENCES durable_tool_identities(id),
    descriptor_json TEXT NOT NULL CHECK (
        json_valid(descriptor_json) AND
        json_type(descriptor_json) = 'object' AND
        length(CAST(descriptor_json AS BLOB)) BETWEEN 2 AND 131072
    ),
    fingerprint TEXT NOT NULL CHECK (
        length(fingerprint) = 64 AND
        fingerprint NOT GLOB '*[^0-9a-f]*'
    ),
    catalog_revision INTEGER NOT NULL CHECK (catalog_revision > 0),
    last_seen_at TEXT NOT NULL,
    retired_at TEXT
) STRICT;

CREATE TABLE server_catalog_issues (
    server_id TEXT NOT NULL REFERENCES servers(id),
    catalog_revision INTEGER NOT NULL CHECK (catalog_revision > 0),
    issue_class TEXT NOT NULL CHECK (issue_class IN ('descriptor_invalid')),
    occurrences INTEGER NOT NULL CHECK (occurrences > 0),
    PRIMARY KEY (server_id, issue_class)
) STRICT;

INSERT INTO server_catalogs (
    server_id, durable_revision, durable_state, durable_tool_count,
    issue_count, last_success_at
)
SELECT id, 0, 'empty', 0, 0, NULL FROM servers;

INSERT INTO schema_migrations (version, name)
VALUES (6, 'catalogs');
