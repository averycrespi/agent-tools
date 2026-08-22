CREATE TABLE keyring_authorities (
    owner TEXT NOT NULL,
    kind TEXT NOT NULL,
    handle TEXT NOT NULL UNIQUE,
    revision INTEGER NOT NULL CHECK (revision >= 0),
    PRIMARY KEY (owner, kind)
) STRICT;

CREATE TABLE keyring_candidates (
    owner TEXT NOT NULL,
    kind TEXT NOT NULL,
    handle TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (owner, kind, handle)
) STRICT;

CREATE INDEX keyring_candidates_cleanup
    ON keyring_candidates (owner, kind, created_at, handle);

INSERT INTO schema_migrations (version, name)
VALUES (3, 'keyring_generations');
