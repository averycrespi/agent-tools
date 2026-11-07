ALTER TABLE git_grants RENAME TO git_grants_previous;

DROP INDEX git_grants_principal;

CREATE TABLE git_grants (
    insertion_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE CHECK (length(id) = 26 AND substr(id, 1, 1) BETWEEN '0' AND '7' AND id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'),
    principal_id TEXT NOT NULL REFERENCES principals(id),
    repository_id TEXT NOT NULL CHECK (length(repository_id) = 26 AND substr(repository_id, 1, 1) BETWEEN '0' AND '7' AND repository_id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'),
    description TEXT CHECK (description IS NULL OR length(CAST(description AS BLOB)) BETWEEN 1 AND 256),
    revision INTEGER NOT NULL CHECK (revision > 0),
    policy_json TEXT NOT NULL CHECK (length(CAST(policy_json AS BLOB)) BETWEEN 1 AND 32768 AND json_valid(policy_json)),
    expires_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

INSERT INTO git_grants SELECT * FROM git_grants_previous;

INSERT INTO sqlite_sequence(name,seq)
SELECT 'git_grants',seq FROM sqlite_sequence WHERE name='git_grants_previous'
AND NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name='git_grants');

UPDATE sqlite_sequence SET seq=max(seq,coalesce((SELECT seq FROM sqlite_sequence WHERE name='git_grants_previous'),0)) WHERE name='git_grants';

DROP TABLE git_grants_previous;

CREATE INDEX git_grants_principal ON git_grants(principal_id);

CREATE TRIGGER git_grants_reserved_target
BEFORE INSERT ON git_grants
WHEN NOT EXISTS (SELECT 1 FROM git_repositories WHERE id=NEW.repository_id)
AND NOT EXISTS (SELECT 1 FROM resource_keys WHERE kind='git_repository' AND resource_id=NEW.repository_id)
BEGIN
    SELECT RAISE(ABORT, 'Git target identity is not reserved');
END;

CREATE TRIGGER git_grants_reserved_target_update
BEFORE UPDATE OF repository_id ON git_grants
WHEN NOT EXISTS (SELECT 1 FROM git_repositories WHERE id=NEW.repository_id)
AND NOT EXISTS (SELECT 1 FROM resource_keys WHERE kind='git_repository' AND resource_id=NEW.repository_id)
BEGIN
    SELECT RAISE(ABORT, 'Git target identity is not reserved');
END;

INSERT INTO schema_migrations (version, name) VALUES (27, 'configuration_targets');
