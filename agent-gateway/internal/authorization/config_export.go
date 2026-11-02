package authorization

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

// Configuration contains no credential metadata. References are internal join
// keys until the export owner substitutes document-local references.
type Configuration struct {
	Agents         []contract.ConfigAgent
	MCPGrants      []contract.ConfigMCPGrant
	HTTPGrants     []contract.ConfigHTTPGrant
	Repositories   []contract.ConfigGitRepository
	GitGrants      []contract.ConfigGitGrant
	RoutingOrigins []string
}

// ConfigurationTx participates in one caller-owned read snapshot. Restricted
// SELECT lists are intentional: credential slots are not export inputs.
func ConfigurationTx(ctx context.Context, tx *sql.Tx) (Configuration, error) {
	out := Configuration{Agents: []contract.ConfigAgent{}, MCPGrants: []contract.ConfigMCPGrant{}, HTTPGrants: []contract.ConfigHTTPGrant{}, Repositories: []contract.ConfigGitRepository{}, GitGrants: []contract.ConfigGitGrant{}, RoutingOrigins: []string{}}
	err := configurationRows(ctx, tx, `SELECT p.id,p.display_name,p.state,p.visibility,d.policy FROM principals p LEFT JOIN http_defaults d ON d.principal_id=p.id ORDER BY p.id LIMIT 129`, func(rows *sql.Rows) error {
		var a contract.ConfigAgent
		if err := rows.Scan(&a.Ref, &a.DisplayName, &a.State, &a.Visibility, &a.HTTPDefault); err != nil {
			return err
		}
		_, stateErr := contract.ParsePrincipalState(string(a.State))
		_, visibilityErr := contract.ParsePrincipalVisibility(string(a.Visibility))
		if !validDisplayName(a.DisplayName) || stateErr != nil || visibilityErr != nil || (a.HTTPDefault != contract.HTTPDefaultAllow && a.HTTPDefault != contract.HTTPDefaultBlock) {
			return ErrInvalidState
		}
		out.Agents = append(out.Agents, a)
		return nil
	})
	if err != nil {
		return out, err
	}
	err = configurationRows(ctx, tx, `SELECT principal_id,server_id,description,effect,upstream_name,constraint_json,read_only,expires_at FROM grants ORDER BY id LIMIT 4097`, func(rows *sql.Rows) error {
		var g contract.ConfigMCPGrant
		var raw sql.NullString
		if err := rows.Scan(&g.Agent, &g.Server, &g.Description, &g.Effect, &g.UpstreamName, &raw, &g.ReadOnly, &g.ExpiresAt); err != nil {
			return err
		}
		g.Constraint = json.RawMessage("null")
		if raw.Valid {
			compiled, err := CompileConstraint([]byte(raw.String))
			if err != nil {
				return err
			}
			g.Constraint = compiled.JSON()
		}
		if !validGrantDescription(g.Description) || !validGrantEffect(g.Effect) || !configurationExpiry(g.ExpiresAt) || g.UpstreamName != nil && !validUpstreamName(*g.UpstreamName) || g.UpstreamName == nil && raw.Valid || g.ReadOnly && (g.Effect != contract.GrantAllow || g.UpstreamName != nil || raw.Valid) {
			return ErrInvalidState
		}
		out.MCPGrants = append(out.MCPGrants, g)
		return nil
	})
	if err != nil {
		return out, err
	}
	err = configurationRows(ctx, tx, `SELECT principal_id,description,policy_json,expires_at FROM http_grants ORDER BY id LIMIT 4097`, func(rows *sql.Rows) error {
		var g contract.ConfigHTTPGrant
		var raw string
		if err := rows.Scan(&g.Agent, &g.Description, &raw, &g.ExpiresAt); err != nil {
			return err
		}
		policy, err := httppolicy.DecodePolicy([]byte(raw))
		if err != nil {
			return err
		}
		canonical, err := policy.JSON()
		if err != nil {
			return err
		}
		var p contract.HTTPPolicy
		if err = json.Unmarshal(canonical, &p); err != nil {
			return err
		}
		g.Policy = contract.ConfigHTTPPolicy{Version: p.Version, Type: p.Type, Destination: p.Destination, Request: p.Request, AllowPrivate: p.AllowPrivate, CredentialRef: p.CredentialID}
		if !validGrantDescription(g.Description) || !configurationExpiry(g.ExpiresAt) {
			return ErrInvalidState
		}
		out.HTTPGrants = append(out.HTTPGrants, g)
		return nil
	})
	if err != nil {
		return out, err
	}
	err = configurationRows(ctx, tx, `SELECT id,name,url,aliases_json,credential_id,deleted FROM git_repositories ORDER BY id LIMIT 1025`, func(rows *sql.Rows) error {
		var r contract.ConfigGitRepository
		var raw string
		if err := rows.Scan(&r.Ref, &r.Name, &r.URL, &raw, &r.CredentialRef, &r.Deleted); err != nil {
			return err
		}
		if err := strictjson.Decode([]byte(raw), &r.Aliases, strictjson.Options{MaxBytes: contract.GitPolicyBytes, MaxDepth: 2}); err != nil {
			return err
		}
		canonical, err := normalizeGitRepository(contract.GitRepositoryDefinition{Name: r.Name, URL: r.URL, Aliases: r.Aliases, CredentialID: r.CredentialRef})
		if err != nil || canonical.URL != r.URL {
			return ErrInvalidState
		}
		out.Repositories = append(out.Repositories, r)
		return nil
	})
	if err != nil {
		return out, err
	}
	err = configurationRows(ctx, tx, `SELECT principal_id,repository_id,description,policy_json,expires_at FROM git_grants ORDER BY id LIMIT 4097`, func(rows *sql.Rows) error {
		var g contract.ConfigGitGrant
		var raw string
		if err := rows.Scan(&g.Agent, &g.Repository, &g.Description, &raw, &g.ExpiresAt); err != nil {
			return err
		}
		var err error
		g.Policy, err = gitpolicy.Decode([]byte(raw))
		if err != nil {
			return err
		}
		if !validGrantDescription(g.Description) || !configurationExpiry(g.ExpiresAt) {
			return ErrInvalidState
		}
		out.GitGrants = append(out.GitGrants, g)
		return nil
	})
	if err != nil {
		return out, err
	}
	var origins string
	if err = tx.QueryRowContext(ctx, `SELECT origins_json FROM git_routing_profile WHERE singleton=1`).Scan(&origins); err != nil {
		return out, err
	}
	if err = strictjson.Decode([]byte(origins), &out.RoutingOrigins, strictjson.Options{MaxBytes: 1024 * 1024, MaxDepth: 2}); err != nil {
		return out, err
	}
	for _, origin := range out.RoutingOrigins {
		canonical, err := gitpolicy.Origin(origin)
		if err != nil || canonical != origin {
			return out, ErrInvalidState
		}
	}
	if len(out.Agents) > 128 || len(out.MCPGrants) > 4096 || len(out.HTTPGrants) > contract.HTTPPolicyGrants || len(out.Repositories) > contract.GitRepositoryIdentities || len(out.GitGrants) > contract.GitGrants {
		return out, ErrResourceLimit
	}
	return out, nil
}

func configurationRows(ctx context.Context, tx *sql.Tx, query string, read func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := read(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func configurationExpiry(value *string) bool {
	if value == nil {
		return true
	}
	_, ok := canonicalTimestamp(*value)
	return ok
}
