package servers

import (
	"context"
	"database/sql"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// ValidateImportedConfiguration uses the same pure transport and capacity owners
// as creation, including retained identities and intended enabled capacity.
func ValidateImportedConfiguration(values []contract.ConfigServer) error {
	active, enabled := 0, 0
	if int64(len(values)) > mustLimit("server_identities") {
		return ErrResourceLimit
	}
	for _, value := range values {
		if value.Deleted {
			if !namespacePattern.MatchString(value.Namespace) || value.Namespace == contract.SyntheticServerNamespace || value.DisplayName == "" || int64(len(value.DisplayName)) > mustLimit("display_name_bytes") {
				return ErrInvalidInput
			}
			continue
		}
		active++
		if value.Enabled {
			enabled++
		}
		transport, err := DecodeTransport(value.Transport)
		if err != nil {
			return err
		}
		if _, err := validateDefinition(Definition{Namespace: value.Namespace, DisplayName: value.DisplayName, Transport: transport}); err != nil {
			return err
		}
	}
	if int64(active) > mustLimit("servers") || int64(enabled) > mustLimit("enabled_servers") {
		return ErrResourceLimit
	}
	return nil
}

// StageOnlineConfigurationTx creates committed desired state without the retired
// stopped-import first-activation fence. Composition triggers normal lifecycle
// work only after the configuration transaction is acknowledged.
func StageOnlineConfigurationTx(ctx context.Context, tx *sql.Tx, values []contract.ConfigServer, ids map[string]string, now time.Time) error {
	if err := ValidateImportedConfiguration(values); err != nil {
		return err
	}
	for _, value := range values {
		id := ids[value.Ref]
		if !validID(id) || id == contract.SyntheticServerID {
			return ErrInvalidInput
		}
		timestamp := formatTime(now)
		var raw any
		var deleted any
		state := contract.DesiredServerDisabled
		if value.Deleted {
			state = contract.DesiredServerDeleted
			deleted = timestamp
		} else {
			transport, err := DecodeTransport(value.Transport)
			if err != nil {
				return err
			}
			encoded, err := canonicalTransport(transport)
			if err != nil {
				return err
			}
			raw = string(encoded)
			if value.Enabled {
				state = contract.DesiredServerEnabled
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO server_identities(id,namespace,created_at) VALUES(?,?,?)`, id, value.Namespace, timestamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO servers(id,display_name,desired_state,desired_revision,transport_json,created_at,updated_at,deleted_at) VALUES(?,?,?,1,?,?,?,?)`, id, value.DisplayName, state, raw, timestamp, timestamp, deleted); err != nil {
			return err
		}
		for _, kind := range []contract.ServerCredentialKind{contract.ServerCredentialStatic, contract.ServerCredentialOAuthClient, contract.ServerCredentialOAuthTokens} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO server_credentials(server_id,kind,revision,handle) VALUES(?,?,0,NULL)`, id, kind); err != nil {
				return err
			}
		}
		for _, statement := range []string{
			`INSERT INTO server_oauth_registrations(server_id,revision) VALUES(?,0)`,
			`INSERT INTO server_operation_watermarks(server_id,pruning_generation) VALUES(?,0)`,
			`INSERT INTO server_auth_flow_watermarks(server_id,pruning_generation) VALUES(?,0)`,
			`INSERT INTO server_catalogs(server_id,durable_revision,durable_state,durable_tool_count,issue_count,last_success_at) VALUES(?,0,'empty',0,0,NULL)`,
		} {
			if _, err := tx.ExecContext(ctx, statement, id); err != nil {
				return err
			}
		}
		if err := audit.MutationTx(ctx, tx, now, "server", "create", contract.AuditTarget{Type: "server", ID: id}); err != nil {
			return err
		}
	}
	return nil
}
