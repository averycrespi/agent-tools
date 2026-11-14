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
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

// ImportedHTTPPolicy binds an already resolved installation-local credential ID.
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

// ValidateImportedConfiguration validates owner-local policy projections and
// cross-domain scope bindings without SQL, secret resolution or external work.
func ValidateImportedConfiguration(value contract.ConfigurationGraph) error {
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

func importedGrantTx(ctx context.Context, tx *sql.Tx, kind, id string, expiry *string, now time.Time) error {
	if expiry != nil {
		parsed, ok := canonicalTimestamp(*expiry)
		if !ok {
			return ErrInvalidInput
		}
		// Exact expiry evidence permits retained expired policies with actual new
		// creation facts, never backdated source history or ordinary-create renewal.
		if !parsed.After(now) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO configuration_import_grant_expiries(kind,id,expires_at) VALUES(?,?,?)`, kind, id, *expiry); err != nil {
				return err
			}
		}
	}
	entity := "grant"
	if kind == "http" {
		entity = "http_grant"
	}
	if kind == "git" {
		entity = "git_grant"
	}
	return audit.MutationTx(ctx, tx, now, entity, "create", contract.AuditTarget{Type: entity, ID: id})
}
