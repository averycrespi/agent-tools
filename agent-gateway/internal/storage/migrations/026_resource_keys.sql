CREATE TABLE resource_keys (
    kind TEXT NOT NULL CHECK (kind IN ('agent', 'mcp_grant', 'http_grant', 'git_grant', 'http_credential', 'git_credential', 'git_repository')),
    resource_id TEXT NOT NULL CHECK (
        length(resource_id) = 26 AND
        substr(resource_id, 1, 1) BETWEEN '0' AND '7' AND
        resource_id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    key TEXT NOT NULL CHECK (
        length(key) BETWEEN 1 AND 64 AND
        key GLOB '[a-z]*' AND
        key NOT GLOB '*[^a-z0-9_-]*'
    ),
    PRIMARY KEY (kind, key),
    UNIQUE (kind, resource_id)
) STRICT;

INSERT INTO resource_keys (kind, resource_id, key)
SELECT 'agent', id, 'agent-' || lower(id) FROM principals
UNION ALL SELECT 'mcp_grant', id, 'mcp-grant-' || lower(id) FROM grants
UNION ALL SELECT 'http_grant', id, 'http-grant-' || lower(id) FROM http_grants
UNION ALL SELECT 'git_grant', id, 'git-grant-' || lower(id) FROM git_grants
UNION ALL SELECT 'http_credential', id, 'http-credential-' || lower(id) FROM http_credentials
UNION ALL SELECT 'git_credential', id, 'git-credential-' || lower(id) FROM git_credentials
UNION ALL SELECT 'git_repository', id, 'git-repository-' || lower(id) FROM git_repositories;

CREATE TRIGGER resource_keys_immutable_update
BEFORE UPDATE ON resource_keys
BEGIN
    SELECT RAISE(ABORT, 'resource keys are immutable');
END;

CREATE TRIGGER resource_keys_immutable_delete
BEFORE DELETE ON resource_keys
BEGIN
    SELECT RAISE(ABORT, 'resource keys are permanent');
END;

INSERT INTO schema_migrations (version, name) VALUES (26, 'resource_keys');
