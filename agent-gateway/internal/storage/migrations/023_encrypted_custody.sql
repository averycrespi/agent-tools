CREATE TABLE secret_custody (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    key_id TEXT NOT NULL CHECK (length(key_id) = 64),
    version INTEGER NOT NULL CHECK (version = 1),
    encryptions INTEGER NOT NULL DEFAULT 0 CHECK (encryptions BETWEEN 0 AND 4294967296)
) STRICT;

CREATE TABLE secret_generations (
    handle TEXT PRIMARY KEY,
    owner TEXT NOT NULL,
    kind TEXT NOT NULL,
    custody TEXT NOT NULL CHECK (custody IN ('legacy', 'encrypted')),
    version INTEGER,
    key_id TEXT,
    ciphertext BLOB,
    CHECK ((custody = 'legacy' AND version IS NULL AND key_id IS NULL AND ciphertext IS NULL)
        OR (custody = 'encrypted' AND version IS NOT NULL AND version = 1
            AND key_id IS NOT NULL AND length(key_id) = 64 AND ciphertext IS NOT NULL
            AND length(ciphertext) BETWEEN 29 AND 262172))
) STRICT;

INSERT INTO secret_generations (handle, owner, kind, custody)
SELECT handle, owner, kind, 'legacy' FROM keyring_authorities
UNION SELECT handle, owner, kind, 'legacy' FROM keyring_candidates;

INSERT INTO schema_migrations (version, name)
VALUES (23, 'encrypted_custody');
