// Package configexport materializes portable configuration from owner-local
// projections in one control snapshot. It has no secret provider or network seam.
package configexport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

type Service struct{ store *storage.Store }

func New(store *storage.Store) *Service { return &Service{store: store} }

func (s *Service) ExportConfiguration(ctx context.Context) ([]byte, error) {
	var document contract.ConfigExport
	err := s.store.View(ctx, func(tx *sql.Tx) error {
		var err error
		document, err = configurationTx(ctx, tx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read portable configuration: %w", err)
	}
	if err = contract.ValidateConfigExport(document); err != nil {
		return nil, err
	}
	body, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("serialize portable configuration: %w", err)
	}
	if len(body)+1 > contract.ConfigExportMaxBytes {
		return nil, fmt.Errorf("portable configuration exceeds document limit")
	}
	return append(body, '\n'), nil
}

// DigestTx binds a stopped import receipt to the complete resulting portable
// graph, independently of input ordering and fresh identity assignment.
func DigestTx(ctx context.Context, tx *sql.Tx) (string, error) {
	value, err := configurationTx(ctx, tx)
	if err != nil {
		return "", err
	}
	if err := contract.ValidateConfigExport(value); err != nil {
		return "", err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func configurationTx(ctx context.Context, tx *sql.Tx) (contract.ConfigExport, error) {
	out := contract.ConfigExport{Format: contract.ConfigExportFormat, Version: contract.ConfigExportVersion}
	serverConfigs, err := servers.ConfigurationTx(ctx, tx)
	if err != nil {
		return out, err
	}
	authority, err := authorization.ConfigurationTx(ctx, tx)
	if err != nil {
		return out, err
	}
	out.HTTP.Credentials, err = httpcredentials.ConfigurationTx(ctx, tx)
	if err != nil {
		return out, err
	}
	out.Git.Credentials, err = gitcredentials.ConfigurationTx(ctx, tx)
	if err != nil {
		return out, err
	}
	out.Agents = authority.Agents
	out.MCP.Servers = servers.SortedConfiguration(serverConfigs)
	out.MCP.Grants = authority.MCPGrants
	out.HTTP.Grants = authority.HTTPGrants
	out.Git.Repositories = authority.Repositories
	out.Git.Grants = authority.GitGrants
	out.Git.RoutingOrigins = authority.RoutingOrigins
	agents := map[string]string{}
	for i := range out.Agents {
		a := &out.Agents[i]
		ref := fmt.Sprintf("agent/%d", i+1)
		agents[a.Ref] = ref
		a.Ref = ref
	}
	httpCredentials := map[string]string{}
	for i := range out.HTTP.Credentials {
		c := &out.HTTP.Credentials[i]
		ref := fmt.Sprintf("http-credential/%d", i+1)
		httpCredentials[c.Ref] = ref
		c.Ref = ref
		c.SecretRef = ref + "/secret"
	}
	gitCredentials := map[string]string{}
	for i := range out.Git.Credentials {
		c := &out.Git.Credentials[i]
		ref := fmt.Sprintf("git-credential/%d", i+1)
		gitCredentials[c.Ref] = ref
		c.Ref = ref
		c.SecretRef = ref + "/secret"
	}
	repositories := map[string]string{}
	for i := range out.Git.Repositories {
		r := &out.Git.Repositories[i]
		ref := fmt.Sprintf("repository/%d", i+1)
		repositories[r.Ref] = ref
		r.Ref = ref
		if r.CredentialRef != nil {
			value, ok := gitCredentials[*r.CredentialRef]
			if !ok {
				return out, fmt.Errorf("unresolved Git credential configuration")
			}
			r.CredentialRef = &value
		}
	}
	for i := range out.MCP.Grants {
		g := &out.MCP.Grants[i]
		g.Agent = agents[g.Agent]
		if g.Server == contract.SyntheticServerID {
			g.Server = "mcp/" + contract.SyntheticServerNamespace
		} else {
			g.Server = serverConfigs[g.Server].Ref
		}
	}
	for i := range out.HTTP.Grants {
		g := &out.HTTP.Grants[i]
		g.Agent = agents[g.Agent]
		if g.Policy.CredentialRef != nil {
			value, ok := httpCredentials[*g.Policy.CredentialRef]
			if !ok {
				return out, fmt.Errorf("unresolved HTTP credential configuration")
			}
			g.Policy.CredentialRef = &value
		}
	}
	for i := range out.Git.Grants {
		g := &out.Git.Grants[i]
		g.Agent = agents[g.Agent]
		g.Repository = repositories[g.Repository]
	}
	return out, nil
}
