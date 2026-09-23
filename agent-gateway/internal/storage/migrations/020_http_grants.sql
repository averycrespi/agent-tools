CREATE TABLE http_defaults (
    principal_id TEXT PRIMARY KEY REFERENCES principals(id),
    policy TEXT NOT NULL CHECK (policy IN ('block', 'allow')),
    revision INTEGER NOT NULL CHECK (revision > 0)
) STRICT;

INSERT INTO http_defaults SELECT id, 'block', 1 FROM principals;

CREATE TRIGGER principal_http_default AFTER INSERT ON principals
BEGIN
    INSERT INTO http_defaults VALUES (NEW.id, 'block', 1);
END;

CREATE TABLE http_grants (
    insertion_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE CHECK (length(id) = 26 AND substr(id, 1, 1) BETWEEN '0' AND '7' AND id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'),
    principal_id TEXT NOT NULL REFERENCES principals(id),
    description TEXT CHECK (description IS NULL OR length(CAST(description AS BLOB)) BETWEEN 1 AND 256),
    revision INTEGER NOT NULL CHECK (revision > 0),
    policy_json TEXT NOT NULL CHECK (length(CAST(policy_json AS BLOB)) BETWEEN 1 AND 16384 AND json_valid(policy_json)),
    expires_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX http_grants_principal ON http_grants(principal_id);

INSERT INTO schema_migrations (version, name) VALUES (20, 'http_grants');
