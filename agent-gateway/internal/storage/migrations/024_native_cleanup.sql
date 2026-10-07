CREATE TABLE native_cleanup (
    handle TEXT PRIMARY KEY,
    owner TEXT NOT NULL,
    kind TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    chunks INTEGER NOT NULL CHECK (chunks BETWEEN 1 AND 117)
) STRICT;

CREATE TABLE native_cleanup_items (
    handle TEXT NOT NULL REFERENCES native_cleanup(handle),
    item INTEGER NOT NULL CHECK (item BETWEEN -1 AND 116),
    state TEXT NOT NULL CHECK (state IN ('retained', 'uncertain', 'deleted', 'reappeared')),
    PRIMARY KEY (handle, item)
) STRICT;

INSERT INTO schema_migrations (version, name) VALUES (24, 'native_cleanup');
