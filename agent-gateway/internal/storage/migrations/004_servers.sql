CREATE TABLE server_identities (
    insertion_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE CHECK (
        length(id) = 26 AND
        substr(id, 1, 1) BETWEEN '0' AND '7' AND
        id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    namespace TEXT NOT NULL UNIQUE CHECK (
        length(namespace) BETWEEN 1 AND 32 AND
        namespace GLOB '[a-z]*' AND
        namespace NOT GLOB '*[^a-z0-9_-]*' AND
        instr(namespace, '.') = 0 AND
        namespace <> 'mcp_gateway'
    ),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE servers (
    id TEXT PRIMARY KEY REFERENCES server_identities(id),
    display_name TEXT NOT NULL CHECK (length(CAST(display_name AS BLOB)) BETWEEN 1 AND 256),
    desired_state TEXT NOT NULL CHECK (desired_state IN ('enabled', 'disabled', 'deleted')),
    desired_revision INTEGER NOT NULL CHECK (desired_revision > 0),
    transport_json TEXT CHECK (transport_json IS NULL OR json_valid(transport_json)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT,
    CHECK ((desired_state = 'deleted') = (transport_json IS NULL)),
    CHECK ((desired_state = 'deleted') = (deleted_at IS NOT NULL))
) STRICT;

CREATE INDEX servers_state_insertion
ON servers (desired_state, id);

CREATE TABLE keyring_authority_fences (
    owner TEXT NOT NULL CHECK (
        length(owner) = 26 AND
        substr(owner, 1, 1) BETWEEN '0' AND '7' AND
        owner NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    kind TEXT NOT NULL CHECK (kind IN ('static_credential', 'oauth_client', 'oauth_tokens')),
    PRIMARY KEY (owner, kind)
) STRICT;

CREATE TABLE server_credentials (
    server_id TEXT NOT NULL REFERENCES servers(id),
    kind TEXT NOT NULL CHECK (kind IN ('static_credential', 'oauth_client', 'oauth_tokens')),
    revision INTEGER NOT NULL CHECK (revision >= 0),
    handle TEXT CHECK (handle IS NULL OR length(handle) BETWEEN 1 AND 512),
    PRIMARY KEY (server_id, kind),
    CHECK (revision > 0 OR handle IS NULL)
) STRICT;

CREATE TABLE server_oauth_registrations (
    server_id TEXT PRIMARY KEY REFERENCES servers(id),
    revision INTEGER NOT NULL CHECK (revision >= 0),
    mode TEXT CHECK (mode IS NULL OR mode IN ('static', 'dynamic')),
    issuer TEXT,
    client_id TEXT,
    callback_url TEXT,
    resource_url TEXT,
    token_endpoint_auth_method TEXT CHECK (
        token_endpoint_auth_method IS NULL OR
        token_endpoint_auth_method IN ('none', 'client_secret_basic', 'client_secret_post')
    ),
    created_at TEXT,
    client_secret_expires_at TEXT,
    CHECK (
        (revision = 0 AND mode IS NULL AND issuer IS NULL AND client_id IS NULL AND
         callback_url IS NULL AND resource_url IS NULL AND token_endpoint_auth_method IS NULL AND
         created_at IS NULL AND client_secret_expires_at IS NULL) OR
        (revision > 0 AND mode IS NOT NULL AND issuer IS NOT NULL AND client_id IS NOT NULL AND
         callback_url IS NOT NULL AND resource_url IS NOT NULL AND
         token_endpoint_auth_method IS NOT NULL AND created_at IS NOT NULL)
    )
) STRICT;

CREATE TABLE server_operations (
    insertion_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE CHECK (
        length(id) = 26 AND
        substr(id, 1, 1) BETWEEN '0' AND '7' AND
        id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    server_id TEXT NOT NULL REFERENCES servers(id),
    kind TEXT NOT NULL CHECK (kind IN (
        'activate', 'reload', 'retry', 'refresh_catalog',
        'credential_replace', 'disable', 'delete', 'disconnect_credentials'
    )),
    target_desired_revision INTEGER NOT NULL CHECK (target_desired_revision > 0),
    target_static_credential_revision INTEGER NOT NULL CHECK (target_static_credential_revision >= 0),
    target_oauth_client_revision INTEGER NOT NULL CHECK (target_oauth_client_revision >= 0),
    target_oauth_tokens_revision INTEGER NOT NULL CHECK (target_oauth_tokens_revision >= 0),
    state TEXT NOT NULL CHECK (state IN (
        'scheduled', 'running', 'succeeded', 'failed',
        'cancelled', 'superseded', 'interrupted'
    )),
    reason TEXT CHECK (reason IS NULL OR reason IN (
        'configuration_invalid', 'resource_limit', 'connectivity', 'tls_failed',
        'protocol_unsupported', 'protocol_invalid', 'authentication_rejected',
        'credential_absent', 'keyring_absent', 'keyring_locked',
        'keyring_interaction_required', 'keyring_unavailable', 'keyring_unsupported',
        'oauth_rejected', 'oauth_expired', 'registration_expired', 'process_exited',
        'output_limit', 'stop_unconfirmed', 'catalog_invalid', 'catalog_limit',
        'catalog_stale', 'superseded', 'cancelled', 'interrupted',
        'revocation_failed', 'revocation_unsupported', 'cleanup_pending'
    )),
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    CHECK ((state IN ('scheduled', 'running')) = (finished_at IS NULL)),
    CHECK (state <> 'running' OR started_at IS NOT NULL)
) STRICT;

CREATE INDEX server_operations_server_insertion
ON server_operations (server_id, insertion_sequence, id);

CREATE TABLE server_operation_watermarks (
    server_id TEXT PRIMARY KEY REFERENCES servers(id),
    pruning_generation INTEGER NOT NULL CHECK (pruning_generation >= 0)
) STRICT;

CREATE TABLE s2_idempotency (
    authority_id TEXT NOT NULL CHECK (
        length(authority_id) = 26 AND
        substr(authority_id, 1, 1) BETWEEN '0' AND '7' AND
        authority_id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    method TEXT NOT NULL,
    route TEXT NOT NULL,
    idempotency_key TEXT NOT NULL CHECK (length(CAST(idempotency_key AS BLOB)) BETWEEN 1 AND 128),
    request_hash BLOB NOT NULL CHECK (length(request_hash) = 32),
    precondition TEXT NOT NULL,
    result_kind TEXT NOT NULL CHECK (result_kind IN ('server', 'operation')),
    server_id TEXT NOT NULL REFERENCES servers(id),
    operation_id TEXT CHECK (
        operation_id IS NULL OR (
            length(operation_id) = 26 AND
            substr(operation_id, 1, 1) BETWEEN '0' AND '7' AND
            operation_id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
        )
    ),
    desired_revision INTEGER NOT NULL CHECK (desired_revision > 0),
    result_json TEXT NOT NULL CHECK (
        json_valid(result_json) AND
        json_type(result_json) = 'object' AND
        length(CAST(result_json AS BLOB)) BETWEEN 2 AND 1048576
    ),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    PRIMARY KEY (authority_id, method, route, idempotency_key),
    CHECK (coalesce(
        (result_kind = 'server' AND operation_id IS NULL AND
         json_type(result_json, '$.server') = 'object' AND
         json_type(result_json, '$.operation') IS NULL) OR
        (result_kind = 'operation' AND operation_id IS NOT NULL AND
         json_type(result_json, '$.operation') = 'object' AND
         json_type(result_json, '$.server') IS NULL),
        false
    ))
) STRICT;

CREATE INDEX s2_idempotency_expiry
ON s2_idempotency (expires_at, authority_id, method, route, idempotency_key);

INSERT INTO schema_migrations (version, name)
VALUES (4, 'servers');
