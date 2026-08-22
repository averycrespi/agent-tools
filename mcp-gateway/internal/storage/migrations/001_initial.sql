CREATE TABLE gateway_meta (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    installation_id TEXT NOT NULL CHECK (
        length(installation_id) = 26 AND
        installation_id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    revision INTEGER NOT NULL CHECK (revision >= 0)
) STRICT;

INSERT INTO gateway_meta (singleton, installation_id, revision)
SELECT 1, installation_id, revision FROM gateway_bootstrap;

CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY CHECK (version > 0),
    name TEXT NOT NULL UNIQUE
) STRICT;

INSERT INTO schema_migrations (version, name) VALUES (1, 'initial');
DROP TABLE gateway_bootstrap;
