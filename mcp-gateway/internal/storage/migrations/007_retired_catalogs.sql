CREATE TABLE server_catalogs_v7 (
    server_id TEXT PRIMARY KEY REFERENCES servers(id),
    durable_revision INTEGER NOT NULL CHECK (durable_revision >= 0),
    durable_state TEXT NOT NULL CHECK (durable_state IN ('empty', 'current', 'stale', 'unavailable', 'retired')),
    durable_tool_count INTEGER NOT NULL CHECK (durable_tool_count >= 0),
    issue_count INTEGER NOT NULL CHECK (issue_count >= 0),
    last_success_at TEXT,
    CHECK ((durable_revision = 0) = (last_success_at IS NULL)),
    CHECK (durable_revision > 0 OR durable_state IN ('empty', 'unavailable', 'retired'))
) STRICT;

INSERT INTO server_catalogs_v7 (
    server_id, durable_revision, durable_state, durable_tool_count,
    issue_count, last_success_at
)
SELECT server_id, durable_revision, durable_state, durable_tool_count,
       issue_count, last_success_at
FROM server_catalogs;

DROP TABLE server_catalogs;
ALTER TABLE server_catalogs_v7 RENAME TO server_catalogs;

INSERT INTO schema_migrations (version, name)
VALUES (7, 'retired_catalogs');
