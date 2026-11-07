package authorization

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/resourcekeys"
)

// ImportedHTTPPolicy substitutes only the installation-local credential identity.
func ImportedHTTPPolicy(value contract.ConfigHTTPPolicy, ids map[string]string) (httppolicy.Policy, []byte, error) {
	var credential *string
	if value.CredentialRef != nil {
		id := ids[*value.CredentialRef]
		credential = &id
	}
	raw, err := json.Marshal(contract.HTTPPolicy{Version: value.Version, Type: value.Type, Destination: value.Destination, Request: value.Request, AllowPrivate: value.AllowPrivate, CredentialID: credential})
	if err != nil {
		return httppolicy.Policy{}, nil, err
	}
	policy, err := httppolicy.DecodePolicy(raw)
	if err != nil {
		return policy, nil, err
	}
	canonical, err := policy.JSON()
	return policy, canonical, err
}

// ValidateImportedConfiguration is pure: it validates all retained policies and
// cross-domain scope bindings before any installation path is created.
func ValidateImportedConfiguration(value contract.ConfigExport) error {
	for _, a := range value.Agents {
		if !validDisplayName(a.DisplayName) {
			return ErrInvalidInput
		}
	}
	expiry := func(value *string) bool {
		if value == nil {
			return true
		}
		_, ok := canonicalTimestamp(*value)
		return ok
	}
	for _, g := range value.MCP.Grants {
		if !validGrantDescription(g.Description) || !expiry(g.ExpiresAt) || g.UpstreamName != nil && !validUpstreamName(*g.UpstreamName) {
			return ErrInvalidInput
		}
		if !bytes.Equal(g.Constraint, []byte("null")) {
			if _, err := CompileConstraint(g.Constraint); err != nil {
				return err
			}
		}
	}
	ids := map[string]string{}
	credentials := map[string]httpcredentials.Definition{}
	for _, c := range value.HTTP.Credentials {
		ids[c.Ref] = "00000000000000000000000001"
		def, err := httpcredentials.Normalize(httpcredentials.Definition{Name: c.Name, Boundary: httpcredentials.Boundary{Host: c.Boundary.Host, Port: c.Boundary.Port, AllowWildcard: c.Boundary.AllowWildcard}, Recipe: c.Recipe})
		if err != nil {
			return err
		}
		credentials[c.Ref] = def
	}
	for _, g := range value.HTTP.Grants {
		if !validGrantDescription(g.Description) || !expiry(g.ExpiresAt) {
			return ErrInvalidInput
		}
		policy, _, err := ImportedHTTPPolicy(g.Policy, ids)
		if err != nil {
			return err
		}
		if g.Policy.CredentialRef != nil {
			def, ok := credentials[*g.Policy.CredentialRef]
			if !ok {
				return ErrInvalidInput
			}
			contained, err := httppolicy.CredentialContains(def.PolicyCredential(contract.HTTPRevisionRef{ID: ids[*g.Policy.CredentialRef], Revision: 1}, false), policy)
			if err != nil || !contained {
				return ErrInvalidInput
			}
		}
	}
	repositories := []storedGitRepository{}
	active := 0
	origins := map[string]string{}
	for _, c := range value.Git.Credentials {
		origins[c.Ref] = c.Origin
	}
	for _, r := range value.Git.Repositories {
		// References are checked by the document owner; the pure domain validator
		// uses a valid placeholder only for the opaque-ID portion of normalization.
		var credential *string
		if r.CredentialRef != nil {
			id := "00000000000000000000000001"
			credential = &id
		}
		def, err := normalizeGitRepository(contract.GitRepositoryDefinition{Name: r.Name, URL: r.URL, Aliases: r.Aliases, CredentialID: credential})
		if err != nil {
			return err
		}
		if r.CredentialRef != nil && origins[*r.CredentialRef] != gitOrigin(def.URL) {
			return ErrInvalidInput
		}
		if err := checkGitLocators(repositories, r.Ref, def); err != nil {
			return err
		}
		repositories = append(repositories, storedGitRepository{resource: contract.GitRepository{ID: r.Ref, GitRepositoryDefinition: def}, deleted: r.Deleted})
		if !r.Deleted {
			active++
		}
	}
	if active > contract.GitRepositories {
		return ErrResourceLimit
	}
	for _, g := range value.Git.Grants {
		if !validGrantDescription(g.Description) || !expiry(g.ExpiresAt) {
			return ErrInvalidInput
		}
		if _, err := gitpolicy.Normalize(g.Policy); err != nil {
			return err
		}
	}
	_, err := normalizeGitOrigins(value.Git.RoutingOrigins)
	return err
}

// ImportedStageFactsTx binds the exact expired-policy exceptions and verifies
// that no imported agent credential authority was installed.
func ImportedStageFactsTx(ctx context.Context, tx *sql.Tx) (string, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM principals WHERE credential_revision<>0 OR credential_id IS NOT NULL OR credential_verifier IS NOT NULL`).Scan(&count); err != nil {
		return "", err
	}
	if count != 0 {
		return "", ErrInvalidState
	}
	rows, err := tx.QueryContext(ctx, `SELECT kind,id,expires_at FROM configuration_import_grant_expiries ORDER BY kind,id`)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var facts strings.Builder
	for rows.Next() {
		var kind, id, expiry string
		if err := rows.Scan(&kind, &id, &expiry); err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(&facts, "%s:%s:%s\n", kind, id, expiry)
	}
	return facts.String(), rows.Err()
}

// StageImportedConfigurationTx preserves exactly the included authority: unlike
// public creation it never synthesizes a default grant or issues a credential.
func StageImportedConfigurationTx(ctx context.Context, tx *sql.Tx, value contract.ConfigExport, ids map[string]string, newID func() (string, error), now time.Time, targets StoredGrantTargetInspector) error {
	if err := ValidateImportedConfiguration(value); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM principals)+(SELECT count(*) FROM grants)+(SELECT count(*) FROM http_grants)+(SELECT count(*) FROM git_grants)+(SELECT count(*) FROM git_repositories)`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrInvalidState
	}
	timestamp := formatAuthorizationTime(now)
	for _, a := range value.Agents {
		id := ids[a.Ref]
		if _, err := resourcekeys.ReserveTx(ctx, tx, resourcekeys.Agent, id, ""); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO principals(id,display_name,state,visibility,revision,credential_revision,created_at,updated_at) VALUES(?,?,?,?,1,0,?,?)`, id, a.DisplayName, a.State, a.Visibility, timestamp, timestamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE http_defaults SET policy=? WHERE principal_id=?`, a.HTTPDefault, id); err != nil {
			return err
		}
		if err := audit.MutationTx(ctx, tx, now, "principal", "create", contract.AuditTarget{Type: "principal", ID: id}); err != nil {
			return err
		}
	}
	for _, r := range value.Git.Repositories {
		var credential *string
		if r.CredentialRef != nil {
			id := ids[*r.CredentialRef]
			credential = &id
		}
		def, err := normalizeGitRepository(contract.GitRepositoryDefinition{Name: r.Name, URL: r.URL, Aliases: r.Aliases, CredentialID: credential})
		if err != nil {
			return err
		}
		aliases, err := json.Marshal(def.Aliases)
		if err != nil {
			return err
		}
		if _, err := resourcekeys.ReserveTx(ctx, tx, resourcekeys.GitRepository, ids[r.Ref], ""); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO git_repositories(id,name,url,aliases_json,credential_id,revision,alias_revision,deleted,created_at,updated_at) VALUES(?,?,?,?,?,1,1,?,?,?)`, ids[r.Ref], def.Name, def.URL, string(aliases), credential, r.Deleted, timestamp, timestamp); err != nil {
			return err
		}
		if err := audit.MutationTx(ctx, tx, now, "git_repository", "create", contract.AuditTarget{Type: "git_repository", ID: ids[r.Ref]}); err != nil {
			return err
		}
	}
	for _, g := range value.MCP.Grants {
		id, err := newID()
		if err != nil {
			return err
		}
		var constraint any
		if !bytes.Equal(g.Constraint, []byte("null")) {
			compiled, err := CompileConstraint(g.Constraint)
			if err != nil {
				return err
			}
			constraint = string(compiled.JSON())
		}
		if _, err := resourcekeys.ReserveTx(ctx, tx, resourcekeys.MCPGrant, id, ""); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO grants(id,description,principal_id,effect,server_id,upstream_name,constraint_json,read_only,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, g.Description, ids[g.Agent], g.Effect, ids[g.Server], g.UpstreamName, constraint, g.ReadOnly, g.ExpiresAt, timestamp); err != nil {
			return err
		}
		if err := importedGrantTx(ctx, tx, "mcp", id, g.ExpiresAt, now); err != nil {
			return err
		}
	}
	for _, g := range value.HTTP.Grants {
		id, err := newID()
		if err != nil {
			return err
		}
		_, raw, err := ImportedHTTPPolicy(g.Policy, ids)
		if err != nil {
			return err
		}
		if _, err := resourcekeys.ReserveTx(ctx, tx, resourcekeys.HTTPGrant, id, ""); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO http_grants(id,principal_id,description,revision,policy_json,expires_at,created_at,updated_at) VALUES(?,?,?,1,?,?,?,?)`, id, ids[g.Agent], g.Description, string(raw), g.ExpiresAt, timestamp, timestamp); err != nil {
			return err
		}
		if err := importedGrantTx(ctx, tx, "http", id, g.ExpiresAt, now); err != nil {
			return err
		}
	}
	for _, g := range value.Git.Grants {
		id, err := newID()
		if err != nil {
			return err
		}
		raw, err := gitpolicy.JSON(g.Policy)
		if err != nil {
			return err
		}
		if _, err := resourcekeys.ReserveTx(ctx, tx, resourcekeys.GitGrant, id, ""); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO git_grants(id,principal_id,repository_id,description,revision,policy_json,expires_at,created_at,updated_at) VALUES(?,?,?,?,1,?,?,?,?)`, id, ids[g.Agent], ids[g.Repository], g.Description, string(raw), g.ExpiresAt, timestamp, timestamp); err != nil {
			return err
		}
		if err := importedGrantTx(ctx, tx, "git", id, g.ExpiresAt, now); err != nil {
			return err
		}
	}
	origins, err := normalizeGitOrigins(value.Git.RoutingOrigins)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(origins)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE git_routing_profile SET origins_json=? WHERE singleton=1`, string(raw)); err != nil {
		return err
	}
	if err := advanceAuthorizationRevisionTx(ctx, tx); err != nil {
		return err
	}
	return validateAuthorityTx(ctx, tx, targets)
}

func importedGrantTx(ctx context.Context, tx *sql.Tx, kind, id string, expiry *string, now time.Time) error {
	if expiry != nil {
		parsed, ok := canonicalTimestamp(*expiry)
		if !ok {
			return ErrInvalidInput
		}
		// This is a fresh import fact, not reconstructed source history. The exact
		// expiry receipt allows already-expired policy without falsifying created_at.
		if !parsed.After(now) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO configuration_import_grant_expiries(kind,id,expires_at) VALUES(?,?,?)`, kind, id, *expiry); err != nil {
				return err
			}
		}
	}
	entity := "grant"
	target := "grant"
	if kind == "http" {
		entity = "http_grant"
		target = "http_grant"
	}
	if kind == "git" {
		entity = "git_grant"
		target = "git_grant"
	}
	return audit.MutationTx(ctx, tx, now, entity, "create", contract.AuditTarget{Type: target, ID: id})
}
