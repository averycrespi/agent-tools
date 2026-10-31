package servers

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// ConfigurationTx reads only desired configuration on the caller's snapshot.
// Map keys are join identities; they must be replaced before document delivery.
func ConfigurationTx(ctx context.Context, tx *sql.Tx) (map[string]contract.ConfigServer, error) {
	rows, err := tx.QueryContext(ctx, `SELECT i.id,i.namespace,s.display_name,s.desired_state,s.transport_json FROM server_identities i JOIN servers s ON s.id=i.id ORDER BY i.namespace LIMIT 1025`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]contract.ConfigServer{}
	for rows.Next() {
		var id string
		var state contract.DesiredServerState
		var raw sql.NullString
		var s contract.ConfigServer
		if err := rows.Scan(&id, &s.Namespace, &s.DisplayName, &state, &raw); err != nil {
			return nil, err
		}
		s.Ref = "mcp/" + s.Namespace
		s.Enabled = state == contract.DesiredServerEnabled
		s.Deleted = state == contract.DesiredServerDeleted
		s.Transport = json.RawMessage("null")
		s.Credentials.Static = map[string]string{}
		if !s.Deleted {
			transport, err := DecodeTransport([]byte(raw.String))
			if err != nil {
				return nil, err
			}
			s.Transport, err = json.Marshal(transport)
			if err != nil {
				return nil, err
			}
			switch t := transport.(type) {
			case contract.StdioTransport:
				for _, slot := range t.SecretEnvironment {
					s.Credentials.Static[slot] = s.Ref + "/static/" + slot
				}
			case contract.StreamableHTTPTransport:
				switch a := t.Authentication.(type) {
				case contract.BearerAuthentication:
					s.Credentials.Static["bearer"] = s.Ref + "/static/bearer"
				case contract.OAuthAuthentication:
					s.Credentials.OAuthAuthorization = true
					if registration, ok := a.Registration.(contract.StaticOAuthRegistration); ok && registration.TokenEndpointAuthMethod != contract.TokenEndpointAuthNone {
						ref := s.Ref + "/oauth/client_secret"
						s.Credentials.OAuthClientSecret = &ref
					}
				}
			}
		} else if raw.Valid {
			return nil, ErrInvalidInput
		}
		out[id] = s
	}
	if len(out) > 1024 {
		return nil, ErrResourceLimit
	}
	return out, rows.Err()
}

// SortedConfiguration avoids exposing internal IDs as either references or order.
func SortedConfiguration(values map[string]contract.ConfigServer) []contract.ConfigServer {
	out := make([]contract.ConfigServer, 0, len(values))
	for _, s := range values {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Namespace < out[j].Namespace })
	return out
}
