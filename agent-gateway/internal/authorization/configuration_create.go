package authorization

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/resourcekeys"
)

// MutateConfiguration owns authority-before-storage ordering for an atomic
// cross-domain configuration create. Callers supply owner-local SQL seams only.
func (r *Repository) MutateConfiguration(ctx context.Context, mutate func(*sql.Tx) error) error {
	return r.mutateAuthorityTx(ctx, "", mutate)
}

// CheckConfigurationRoutingTx is advisory in a preview and authoritative only
// under MutateConfiguration. The seeded singleton may be initialized once.
func (r *Repository) CheckConfigurationRoutingTx(ctx context.Context, tx *sql.Tx, origins []string) error {
	profile, err := gitRoutingProfileTx(ctx, tx)
	if err != nil {
		return err
	}
	var historical bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM configuration_import)`).Scan(&historical); err != nil {
		return err
	}
	if profile.Revision != "1" || len(profile.Origins) != 0 || historical {
		return ErrConflict
	}
	canonical, err := normalizeGitOrigins(origins)
	if err != nil {
		return err
	}
	r.authority.mu.Lock()
	defer r.authority.mu.Unlock()
	for _, origin := range canonical {
		if r.authority.opaqueGitOrigins[origin] != 0 {
			return ErrConflict
		}
	}
	return nil
}

// StageConfigurationTx creates only submitted authority definitions; dependency
// IDs include existing resources but never cause their creation or modification.
// Whole-domain validation is performed against the combined graph by the caller.
func (r *Repository) StageConfigurationTx(ctx context.Context, tx *sql.Tx, definitions []contract.ConfigDefinition, ids map[string]string, now time.Time, targets StoredGrantTargetInspector) error {
	stamp := formatAuthorizationTime(now)
	ref := func(v contract.ConfigReference) string {
		if v.Kind == "gateway_mcp" {
			return contract.SyntheticServerID
		}
		return ids[v.Kind+"/"+v.Key]
	}
	for _, kind := range []string{"agent", "git_repository", "mcp_grant", "http_grant", "git_grant", "git_routing"} {
		for _, d := range definitions {
			if d.Kind != kind || d.Reserved {
				continue
			}
			id := ids[d.Kind+"/"+d.Key]
			if kind != "git_routing" {
				if _, err := resourcekeys.ReserveTx(ctx, tx, resourcekeys.Kind(kind), id, d.Key); err != nil {
					return err
				}
			}
			switch kind {
			case "agent":
				var s contract.ConfigAgentSpec
				if json.Unmarshal(d.Spec, &s) != nil {
					return ErrInvalidInput
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO principals(id,display_name,state,visibility,revision,credential_revision,created_at,updated_at) VALUES(?,?,?,?,1,0,?,?)`, id, s.DisplayName, s.State, s.Visibility, stamp, stamp); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE http_defaults SET policy=? WHERE principal_id=?`, s.HTTPDefault, id); err != nil {
					return err
				}
				if err := audit.MutationTx(ctx, tx, now, "principal", "create", contract.AuditTarget{Type: "principal", ID: id}); err != nil {
					return err
				}
			case "git_repository":
				var s contract.ConfigGitRepositorySpec
				if json.Unmarshal(d.Spec, &s) != nil {
					return ErrInvalidInput
				}
				var credential *string
				if s.Credential != nil {
					v := ref(*s.Credential)
					credential = &v
				}
				def, err := normalizeGitRepository(contract.GitRepositoryDefinition{Name: s.Name, URL: s.URL, Aliases: s.Aliases, CredentialID: credential})
				if err != nil {
					return err
				}
				aliases, err := json.Marshal(def.Aliases)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO git_repositories(id,name,url,aliases_json,credential_id,revision,alias_revision,deleted,created_at,updated_at) VALUES(?,?,?,?,?,1,1,?,?,?)`, id, def.Name, def.URL, string(aliases), credential, s.Deleted, stamp, stamp); err != nil {
					return err
				}
				if err := audit.MutationTx(ctx, tx, now, "git_repository", "create", contract.AuditTarget{Type: "git_repository", ID: id}); err != nil {
					return err
				}
			case "mcp_grant":
				var s contract.ConfigMCPGrantSpec
				if json.Unmarshal(d.Spec, &s) != nil {
					return ErrInvalidInput
				}
				var constraint any
				if !bytes.Equal(bytes.TrimSpace(s.Constraint), []byte("null")) {
					c, err := CompileConstraint(s.Constraint)
					if err != nil {
						return err
					}
					constraint = string(c.JSON())
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO grants(id,description,principal_id,effect,server_id,upstream_name,constraint_json,read_only,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, s.Description, ref(s.Agent), s.Effect, ref(s.Server), s.UpstreamName, constraint, s.ReadOnly, s.ExpiresAt, stamp); err != nil {
					return err
				}
				if err := importedGrantTx(ctx, tx, "mcp", id, s.ExpiresAt, now); err != nil {
					return err
				}
			case "http_grant":
				var s contract.ConfigHTTPGrantSpec
				if json.Unmarshal(d.Spec, &s) != nil {
					return ErrInvalidInput
				}
				var credential *string
				if s.Policy.Credential != nil {
					v := s.Policy.Credential.Kind + "/" + s.Policy.Credential.Key
					credential = &v
				}
				_, raw, err := ImportedHTTPPolicy(contract.ConfigHTTPPolicy{Version: s.Policy.Version, Type: s.Policy.Type, Destination: s.Policy.Destination, Request: s.Policy.Request, AllowPrivate: s.Policy.AllowPrivate, CredentialRef: credential}, ids)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO http_grants(id,principal_id,description,revision,policy_json,expires_at,created_at,updated_at) VALUES(?,?,?,1,?,?,?,?)`, id, ref(s.Agent), s.Description, string(raw), s.ExpiresAt, stamp, stamp); err != nil {
					return err
				}
				if err := importedGrantTx(ctx, tx, "http", id, s.ExpiresAt, now); err != nil {
					return err
				}
			case "git_grant":
				var s contract.ConfigGitGrantSpec
				if json.Unmarshal(d.Spec, &s) != nil {
					return ErrInvalidInput
				}
				raw, err := gitpolicy.JSON(s.Policy)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO git_grants(id,principal_id,repository_id,description,revision,policy_json,expires_at,created_at,updated_at) VALUES(?,?,?,?,1,?,?,?,?)`, id, ref(s.Agent), ref(s.Repository), s.Description, string(raw), s.ExpiresAt, stamp, stamp); err != nil {
					return err
				}
				if err := importedGrantTx(ctx, tx, "git", id, s.ExpiresAt, now); err != nil {
					return err
				}
			case "git_routing":
				var s contract.ConfigGitRoutingSpec
				if json.Unmarshal(d.Spec, &s) != nil {
					return ErrInvalidInput
				}
				if err := r.CheckConfigurationRoutingTx(ctx, tx, s.Origins); err != nil {
					return err
				}
				origins, err := normalizeGitOrigins(s.Origins)
				if err != nil {
					return err
				}
				raw, err := json.Marshal(origins)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE git_routing_profile SET origins_json=?,revision=revision+1 WHERE singleton=1`, string(raw)); err != nil {
					return err
				}
				var installation string
				if err := tx.QueryRowContext(ctx, `SELECT installation_id FROM gateway_meta WHERE singleton=1`).Scan(&installation); err != nil {
					return err
				}
				if err := audit.MutationTx(ctx, tx, now, "git_profile", "update", contract.AuditTarget{Type: "installation", ID: installation}); err != nil {
					return err
				}
			}
		}
	}
	if err := advanceAuthorizationRevisionTx(ctx, tx); err != nil {
		return err
	}
	return validateAuthorityTx(ctx, tx, targets)
}
