CREATE TABLE server_auth_flows (
    insertion_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE CHECK (
        length(id) = 26 AND
        substr(id, 1, 1) BETWEEN '0' AND '7' AND
        id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    server_id TEXT NOT NULL REFERENCES servers(id),
    flow_state TEXT NOT NULL CHECK (flow_state IN (
        'preparing', 'awaiting_callback', 'exchanging', 'succeeded',
        'failed', 'expired', 'cancelled', 'superseded', 'interrupted'
    )),
    target_desired_revision INTEGER NOT NULL CHECK (target_desired_revision > 0),
    registration_revision INTEGER NOT NULL CHECK (registration_revision >= 0),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    finished_at TEXT,
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
    CHECK ((flow_state IN ('preparing', 'awaiting_callback', 'exchanging')) = (finished_at IS NULL))
) STRICT;

CREATE INDEX server_auth_flows_server_insertion
ON server_auth_flows (server_id, insertion_sequence, id);

CREATE INDEX server_auth_flows_active_expiry
ON server_auth_flows (flow_state, expires_at, server_id);

CREATE TABLE server_auth_flow_watermarks (
    server_id TEXT PRIMARY KEY REFERENCES servers(id),
    pruning_generation INTEGER NOT NULL CHECK (pruning_generation >= 0)
) STRICT;

INSERT INTO server_auth_flow_watermarks (server_id, pruning_generation)
SELECT id, 0 FROM servers;

INSERT INTO schema_migrations (version, name)
VALUES (5, 'auth_flows');
