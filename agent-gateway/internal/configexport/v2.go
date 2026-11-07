package configexport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/configdefinition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/resourcekeys"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
)

var ErrConfigurationNotFound = errors.New("configuration definition not found")

// ExportBundle emits the complete retained v2 graph in one coherent snapshot.
// It is creation input, not an authoritative file or an installation backup.
// The old ExportConfiguration route/init pair is transitional until TOOLS-151.
func (s *Service) ExportBundle(ctx context.Context) ([]byte, error) {
	var bundle contract.ConfigBundle
	err := s.store.View(ctx, func(tx *sql.Tx) error { var err error; bundle, err = configurationV2Tx(ctx, tx); return err })
	if err != nil {
		return nil, fmt.Errorf("read version-2 configuration: %w", err)
	}
	if configdefinition.ValidateBundle(bundle) != nil {
		return nil, configdefinition.ErrInvalid
	}
	return marshalV2(bundle)
}

// ExportResource emits exactly one definition; no dependencies are added. The
// caller selects a typed public key, or git_routing with an empty singleton key.
// A permanently removed resource exports only its reservation, not resurrection.
func (s *Service) ExportResource(ctx context.Context, kind, key string) ([]byte, error) {
	var found *contract.ConfigDefinition
	err := s.store.View(ctx, func(tx *sql.Tx) error {
		bundle, err := configurationV2KindTx(ctx, tx, kind)
		if err != nil {
			return err
		}
		for _, d := range bundle.Resources {
			if d.Kind == kind && d.Key == key {
				found = &d
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read version-2 resource configuration: %w", err)
	}
	if found == nil {
		return nil, ErrConfigurationNotFound
	}
	if _, err := configdefinition.Normalize(*found); err != nil {
		return nil, err
	}
	return marshalV2(found)
}
func marshalV2(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(raw)+1 > contract.ConfigExportMaxBytes {
		return nil, configdefinition.ErrInvalid
	}
	return append(raw, '\n'), nil
}

func configurationV2Tx(ctx context.Context, tx *sql.Tx) (contract.ConfigBundle, error) {
	return configurationV2KindTx(ctx, tx, "")
}

func configurationV2KindTx(ctx context.Context, tx *sql.Tx, kind string) (contract.ConfigBundle, error) {
	out := contract.ConfigBundle{Format: contract.ConfigExportFormat, Version: contract.ConfigV2Version, Resources: []contract.ConfigDefinition{}}
	keys, err := resourcekeys.ConfigurationTx(ctx, tx)
	if err != nil {
		return out, err
	}
	bindings := map[string]string{}
	for _, k := range keys {
		bindings[string(k.Kind)+"/"+k.ResourceID] = k.Key
	}
	resolve := func(kind, id string) (contract.ConfigReference, error) {
		key, ok := bindings[kind+"/"+id]
		if !ok {
			return contract.ConfigReference{}, errors.New("missing configuration key reservation")
		}
		return contract.ConfigReference{Kind: kind, Key: key}, nil
	}
	seen := map[string]bool{}
	add := func(kind, key string, spec any) error {
		raw, err := json.Marshal(spec)
		if err != nil {
			return err
		}
		out.Resources = append(out.Resources, contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: contract.ConfigV2Version, Kind: kind, Key: key, Spec: raw})
		seen[kind+"/"+key] = true
		return nil
	}
	authority, err := authorization.ConfigurationForKindTx(ctx, tx, kind)
	if err != nil {
		return out, err
	}
	serverConfigs := map[string]contract.ConfigServer{}
	switch kind {
	case "", "mcp_server":
		serverConfigs, err = servers.ConfigurationTx(ctx, tx)
	case "mcp_grant":
		serverConfigs, err = servers.ConfigurationReferencesTx(ctx, tx)
	}
	if err != nil {
		return out, err
	}
	httpConfigs := map[string]contract.ConfigHTTPCredentialSpec{}
	if kind == "" || kind == "http_credential" {
		httpConfigs, err = httpcredentials.RetainedConfigurationTx(ctx, tx)
	}
	if err != nil {
		return out, err
	}
	gitConfigs := map[string]contract.ConfigGitCredentialSpec{}
	if kind == "" || kind == "git_credential" {
		gitConfigs, err = gitcredentials.RetainedConfigurationTx(ctx, tx)
	}
	if err != nil {
		return out, err
	}
	for _, a := range authority.Agents {
		ref, err := resolve("agent", a.Ref)
		if err != nil {
			return out, err
		}
		if err := add("agent", ref.Key, contract.ConfigAgentSpec{DisplayName: a.DisplayName, State: a.State, Visibility: a.Visibility, HTTPDefault: a.HTTPDefault}); err != nil {
			return out, err
		}
	}
	serverKeys := serverConfigs
	if kind == "" || kind == "mcp_server" {
		serverKeys, err = servers.ConfigurationReferencesTx(ctx, tx)
		if err != nil {
			return out, err
		}
		for id, target := range serverKeys {
			if _, exists := serverConfigs[id]; !exists {
				out.Resources = append(out.Resources, contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: contract.ConfigV2Version, Kind: "mcp_server", Key: target.Namespace, Reserved: true, Spec: json.RawMessage("null")})
			}
		}
	}
	for _, s := range serverConfigs {
		if kind == "mcp_grant" {
			continue
		}
		if err := add("mcp_server", s.Namespace, contract.ConfigServerSpec{DisplayName: s.DisplayName, Enabled: s.Enabled, Deleted: s.Deleted, Transport: s.Transport, Credentials: s.Credentials}); err != nil {
			return out, err
		}
	}
	for id, c := range httpConfigs {
		ref, err := resolve("http_credential", id)
		if err != nil {
			return out, err
		}
		if !c.Deleted {
			secret := "http_credential/" + ref.Key + "/secret"
			c.SecretRef = &secret
		}
		if err := add(ref.Kind, ref.Key, c); err != nil {
			return out, err
		}
	}
	for id, c := range gitConfigs {
		ref, err := resolve("git_credential", id)
		if err != nil {
			return out, err
		}
		if !c.Deleted {
			secret := "git_credential/" + ref.Key + "/secret"
			c.SecretRef = &secret
		}
		if err := add(ref.Kind, ref.Key, c); err != nil {
			return out, err
		}
	}
	for _, g := range authority.MCPGrants {
		ref, err := resolve("mcp_grant", g.ID)
		if err != nil {
			return out, err
		}
		agent, err := resolve("agent", g.Agent)
		if err != nil {
			return out, err
		}
		server := contract.ConfigReference{Kind: "gateway_mcp"}
		if g.Server != contract.SyntheticServerID {
			s, ok := serverKeys[g.Server]
			if !ok {
				return out, errors.New("missing MCP configuration target")
			}
			server = contract.ConfigReference{Kind: "mcp_server", Key: s.Namespace}
		}
		if err := add(ref.Kind, ref.Key, contract.ConfigMCPGrantSpec{Agent: agent, Server: server, Description: g.Description, Effect: g.Effect, UpstreamName: g.UpstreamName, Constraint: g.Constraint, ReadOnly: g.ReadOnly, ExpiresAt: g.ExpiresAt}); err != nil {
			return out, err
		}
	}
	for _, g := range authority.HTTPGrants {
		ref, err := resolve("http_grant", g.ID)
		if err != nil {
			return out, err
		}
		agent, err := resolve("agent", g.Agent)
		if err != nil {
			return out, err
		}
		var credential *contract.ConfigReference
		if g.Policy.CredentialRef != nil {
			c, err := resolve("http_credential", *g.Policy.CredentialRef)
			if err != nil {
				return out, err
			}
			credential = &c
		}
		if err := add(ref.Kind, ref.Key, contract.ConfigHTTPGrantSpec{Agent: agent, Description: g.Description, ExpiresAt: g.ExpiresAt, Policy: contract.ConfigV2HTTPPolicy{Version: g.Policy.Version, Type: g.Policy.Type, Destination: g.Policy.Destination, Request: g.Policy.Request, AllowPrivate: g.Policy.AllowPrivate, Credential: credential}}); err != nil {
			return out, err
		}
	}
	for _, r := range authority.Repositories {
		ref, err := resolve("git_repository", r.Ref)
		if err != nil {
			return out, err
		}
		var credential *contract.ConfigReference
		if r.CredentialRef != nil {
			c, err := resolve("git_credential", *r.CredentialRef)
			if err != nil {
				return out, err
			}
			credential = &c
		}
		if err := add(ref.Kind, ref.Key, contract.ConfigGitRepositorySpec{Name: r.Name, URL: r.URL, Aliases: r.Aliases, Credential: credential, Deleted: r.Deleted}); err != nil {
			return out, err
		}
	}
	for _, g := range authority.GitGrants {
		ref, err := resolve("git_grant", g.ID)
		if err != nil {
			return out, err
		}
		agent, err := resolve("agent", g.Agent)
		if err != nil {
			return out, err
		}
		repo, err := resolve("git_repository", g.Repository)
		if err != nil {
			return out, err
		}
		if err := add(ref.Kind, ref.Key, contract.ConfigGitGrantSpec{Agent: agent, Repository: repo, Description: g.Description, Policy: g.Policy, ExpiresAt: g.ExpiresAt}); err != nil {
			return out, err
		}
	}
	if kind == "" || kind == "git_routing" {
		if err := add("git_routing", "", contract.ConfigGitRoutingSpec{Origins: authority.RoutingOrigins}); err != nil {
			return out, err
		}
	}
	for _, k := range keys {
		if kind != "" && kind != string(k.Kind) {
			continue
		}
		kind := string(k.Kind)
		if !seen[kind+"/"+k.Key] {
			out.Resources = append(out.Resources, contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: contract.ConfigV2Version, Kind: kind, Key: k.Key, Reserved: true, Spec: json.RawMessage("null")})
		}
	}
	sort.Slice(out.Resources, func(i, j int) bool {
		a, b := out.Resources[i], out.Resources[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Key < b.Key
	})
	return out, nil
}
