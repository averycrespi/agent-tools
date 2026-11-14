package configimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/configdefinition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/configexport"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/resourcekeys"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

var ErrConflict = errors.New("configuration key, namespace or singleton already exists or is reserved")

type Service struct {
	store     *storage.Store
	authority *authorization.Repository
	targets   *servers.Repository
	now       func() time.Time
	newID     func() (string, error)
	committed func(context.Context, []contract.ConfigCreated)
}

// NewOnline has no material/network capability. The composition-owned callback
// runs only after a successful configuration commit; it cannot roll it back.
func NewOnline(store *storage.Store, authority *authorization.Repository, targets *servers.Repository, now func() time.Time, newID func() (string, error), committed func(context.Context, []contract.ConfigCreated)) *Service {
	return &Service{store: store, authority: authority, targets: targets, now: now, newID: newID, committed: committed}
}

func (s *Service) Preview(ctx context.Context, raw []byte) (contract.ConfigPreview, error) {
	defs, err := configdefinition.DecodeSubmission(raw)
	if err != nil {
		return contract.ConfigPreview{}, err
	}
	err = s.store.View(ctx, func(tx *sql.Tx) error { return s.validateTx(ctx, tx, defs) })
	if err != nil {
		return contract.ConfigPreview{}, err
	}
	return preview(defs), nil
}

func (s *Service) Import(ctx context.Context, raw []byte) (contract.ConfigImportResult, error) {
	defs, err := configdefinition.DecodeSubmission(raw)
	if err != nil {
		return contract.ConfigImportResult{}, err
	}
	result := contract.ConfigImportResult{Configuration: "committed", Creations: []contract.ConfigCreated{}, Activation: "not_required"}
	if len(defs) == 0 {
		return result, nil
	}
	for _, d := range defs {
		if d.Kind == "mcp_server" && !d.Reserved {
			var spec contract.ConfigServerSpec
			_ = json.Unmarshal(d.Spec, &spec)
			if spec.Enabled {
				result.Activation = "pending"
			}
		}
	}
	err = s.authority.MutateConfiguration(ctx, func(tx *sql.Tx) error {
		if err := s.validateTx(ctx, tx, defs); err != nil {
			return err
		}
		ids := map[string]string{}
		keys, err := resourcekeys.ConfigurationTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, k := range keys {
			ids[string(k.Kind)+"/"+k.Key] = k.ResourceID
		}
		targets, err := servers.ConfigurationReferencesTx(ctx, tx)
		if err != nil {
			return err
		}
		for id, t := range targets {
			ids["mcp_server/"+t.Namespace] = id
		}
		for _, d := range defs {
			id := ""
			if d.Kind != "git_routing" {
				id, err = s.newID()
				if err != nil {
					return err
				}
				if !contract.ValidAuditID(id) {
					return configdefinition.ErrInvalid
				}
				ids[d.Kind+"/"+d.Key] = id
			}
			result.Creations = append(result.Creations, contract.ConfigCreated{Kind: d.Kind, Key: d.Key, ID: id, Reserved: d.Reserved})
		}
		now := s.now()
		// Bind every reservation before dependent grants. Reserved definitions never
		// synthesize a domain row, material, token, or implicit authority.
		for _, d := range defs {
			if !d.Reserved {
				continue
			}
			id := ids[d.Kind+"/"+d.Key]
			if d.Kind == "mcp_server" {
				err = servers.ReserveConfigurationTargetTx(ctx, tx, id, d.Key, now)
			} else {
				err = resourcekeys.ReserveConfigurationTx(ctx, tx, resourcekeys.Kind(d.Kind), id, d.Key, now)
			}
			if err != nil {
				return err
			}
		}
		values := []contract.ConfigServer{}
		for _, d := range defs {
			if d.Reserved {
				continue
			}
			switch d.Kind {
			case "mcp_server":
				var v contract.ConfigServerSpec
				_ = json.Unmarshal(d.Spec, &v)
				values = append(values, contract.ConfigServer{Ref: d.Kind + "/" + d.Key, Namespace: d.Key, DisplayName: v.DisplayName, Enabled: v.Enabled, Deleted: v.Deleted, Transport: v.Transport, Credentials: v.Credentials})
			case "http_credential":
				var v contract.ConfigHTTPCredentialSpec
				_ = json.Unmarshal(d.Spec, &v)
				err = httpcredentials.StageConfigurationDefinitionTx(ctx, tx, ids[d.Kind+"/"+d.Key], httpcredentials.Definition{Key: d.Key, Name: v.Name, Boundary: httpcredentials.Boundary{Host: v.Boundary.Host, Port: v.Boundary.Port, AllowWildcard: v.Boundary.AllowWildcard}, Recipe: v.Recipe}, v.Deleted, now)
			case "git_credential":
				var v contract.ConfigGitCredentialSpec
				_ = json.Unmarshal(d.Spec, &v)
				v.Key = d.Key
				err = gitcredentials.StageConfigurationDefinitionTx(ctx, tx, ids[d.Kind+"/"+d.Key], v.GitCredentialDefinition, v.Deleted, now)
			}
			if err != nil {
				return err
			}
		}
		if err := servers.StageOnlineConfigurationTx(ctx, tx, values, ids, now); err != nil {
			return err
		}
		return s.authority.StageConfigurationTx(ctx, tx, defs, ids, now, s.targets)
	})
	if err != nil {
		return contract.ConfigImportResult{}, err
	}
	if s.committed != nil {
		s.committed(ctx, result.Creations)
	}
	return result, nil
}

func (s *Service) validateTx(ctx context.Context, tx *sql.Tx, defs []contract.ConfigDefinition) error {
	existing, err := configexport.BundleTx(ctx, tx)
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, d := range existing.Resources {
		present[d.Kind+"/"+d.Key] = true
	}
	combined := contract.ConfigBundle{Format: contract.ConfigExportFormat, Version: contract.ConfigV2Version, Resources: []contract.ConfigDefinition{}}
	routing := false
	for _, d := range defs {
		if d.Kind == "git_routing" {
			var v contract.ConfigGitRoutingSpec
			_ = json.Unmarshal(d.Spec, &v)
			if err := s.authority.CheckConfigurationRoutingTx(ctx, tx, v.Origins); err != nil {
				if errors.Is(err, authorization.ErrConflict) {
					return ErrConflict
				}
				return err
			}
			routing = true
		} else if present[d.Kind+"/"+d.Key] {
			return ErrConflict
		}
		combined.Resources = append(combined.Resources, d)
	}
	for _, d := range existing.Resources {
		if routing && d.Kind == "git_routing" {
			continue
		}
		combined.Resources = append(combined.Resources, d)
	}
	return configdefinition.ValidateBundle(combined)
}

func preview(defs []contract.ConfigDefinition) contract.ConfigPreview {
	out := contract.ConfigPreview{Advisory: true, Creations: []contract.ConfigCreation{}}
	for _, d := range defs {
		creation := contract.ConfigCreation{Kind: d.Kind, Key: d.Key, Reserved: d.Reserved, Dependencies: []contract.ConfigReference{}, Requirements: []string{}}
		if !d.Reserved {
			switch d.Kind {
			case "agent":
				creation.Requirements = append(creation.Requirements, "issue_agent_credential")
			case "http_credential", "git_credential":
				var state struct {
					Deleted bool `json:"deleted"`
				}
				_ = json.Unmarshal(d.Spec, &state)
				if !state.Deleted {
					creation.Requirements = append(creation.Requirements, "supply_credential_value")
				}
			case "mcp_server":
				var v contract.ConfigServerSpec
				_ = json.Unmarshal(d.Spec, &v)
				if len(v.Credentials.Static) > 0 {
					creation.Requirements = append(creation.Requirements, "supply_static_credentials")
				}
				if v.Credentials.OAuthClientSecret != nil {
					creation.Requirements = append(creation.Requirements, "supply_oauth_client_secret")
				}
				if v.Credentials.OAuthAuthorization {
					creation.Requirements = append(creation.Requirements, "authorize_oauth_explicitly")
				}
				if v.Enabled {
					creation.Requirements = append(creation.Requirements, "activate_after_commit")
				}
			case "git_routing":
				creation.Requirements = append(creation.Requirements, "initialize_untouched_routing_seed")
			}
			var refs struct {
				Agent      *contract.ConfigReference `json:"agent"`
				Server     *contract.ConfigReference `json:"server"`
				Repository *contract.ConfigReference `json:"repository"`
				Credential *contract.ConfigReference `json:"credential"`
				Policy     struct {
					Credential *contract.ConfigReference `json:"credential"`
				} `json:"policy"`
			}
			_ = json.Unmarshal(d.Spec, &refs)
			for _, r := range []*contract.ConfigReference{refs.Agent, refs.Server, refs.Repository, refs.Credential, refs.Policy.Credential} {
				if r != nil {
					creation.Dependencies = append(creation.Dependencies, *r)
				}
			}
		}
		out.Creations = append(out.Creations, creation)
	}
	return out
}
