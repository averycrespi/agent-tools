ALTER TABLE server_auth_flows ADD COLUMN diagnostic_stage TEXT CHECK (
    diagnostic_stage IS NULL OR diagnostic_stage IN (
        'metadata_discovery', 'client_registration', 'authorization_request',
        'callback_validation', 'token_exchange', 'credential_installation'
    )
);

ALTER TABLE server_auth_flows ADD COLUMN diagnostic_http_status INTEGER CHECK (
    diagnostic_http_status IS NULL OR diagnostic_http_status BETWEEN 100 AND 599
);

CREATE TRIGGER server_auth_flows_diagnostic_insert
BEFORE INSERT ON server_auth_flows
WHEN
    (NEW.diagnostic_stage IS NOT NULL AND (NEW.flow_state != 'failed' OR NEW.reason IS NULL)) OR
    (NEW.diagnostic_http_status IS NOT NULL AND NEW.diagnostic_stage IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'invalid OAuth diagnostic');
END;

CREATE TRIGGER server_auth_flows_diagnostic_update
BEFORE UPDATE ON server_auth_flows
WHEN
    (NEW.diagnostic_stage IS NOT NULL AND (NEW.flow_state != 'failed' OR NEW.reason IS NULL)) OR
    (NEW.diagnostic_http_status IS NOT NULL AND NEW.diagnostic_stage IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'invalid OAuth diagnostic');
END;

INSERT INTO schema_migrations (version, name)
VALUES (11, 'oauth_diagnostics');
