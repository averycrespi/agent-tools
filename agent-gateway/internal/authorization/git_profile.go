package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

func normalizeGitOrigins(origins []string) ([]string, error) {
	if origins == nil || len(origins) > contract.GitRepositories {
		return nil, ErrInvalidInput
	}
	out := []string{}
	for _, raw := range origins {
		origin, err := gitpolicy.Origin(raw)
		if err != nil || slices.Contains(out, origin) {
			return nil, ErrInvalidInput
		}
		out = append(out, origin)
	}
	slices.Sort(out)
	return out, nil
}

func gitRoutingProfileTx(ctx context.Context, tx *sql.Tx) (contract.GitRoutingProfile, error) {
	var profile contract.GitRoutingProfile
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT origins_json,revision FROM git_routing_profile WHERE singleton=1`).Scan(&raw, &profile.Revision); err != nil {
		return profile, err
	}
	if strictjson.Decode([]byte(raw), &profile.Origins, strictjson.Options{MaxBytes: 1048576, MaxDepth: 2}) != nil || !validRevision(profile.Revision) {
		return profile, ErrInvalidState
	}
	canonical, err := normalizeGitOrigins(profile.Origins)
	encoded, _ := json.Marshal(canonical)
	if err != nil || string(encoded) != raw {
		return profile, ErrInvalidState
	}
	return profile, nil
}

func (r *Repository) GetGitRoutingProfile(ctx context.Context) (profile contract.GitRoutingProfile, err error) {
	err = r.view(ctx, func(tx *sql.Tx) error { var e error; profile, e = gitRoutingProfileTx(ctx, tx); return e })
	return
}

func (r *Repository) PutGitRoutingProfile(ctx context.Context, revision string, origins []string) (contract.GitRoutingProfile, error) {
	canonical, err := normalizeGitOrigins(origins)
	if err != nil || !validRevision(revision) {
		return contract.GitRoutingProfile{}, ErrInvalidInput
	}
	raw, _ := json.Marshal(canonical)
	var result contract.GitRoutingProfile
	err = r.mutateAuthorityTx(ctx, "", func(tx *sql.Tx) error {
		current, e := gitRoutingProfileTx(ctx, tx)
		if e != nil {
			return e
		}
		if current.Revision != revision {
			return ErrStaleRevision
		}
		if _, e := tx.ExecContext(ctx, `UPDATE git_routing_profile SET origins_json=?,revision=revision+1 WHERE singleton=1`, string(raw)); e != nil {
			return e
		}
		if e := advanceAuthorizationRevisionTx(ctx, tx); e != nil {
			return e
		}
		result, e = gitRoutingProfileTx(ctx, tx)
		if e != nil {
			return e
		}
		var installationID string
		if e := tx.QueryRowContext(ctx, `SELECT installation_id FROM gateway_meta WHERE singleton=1`).Scan(&installationID); e != nil {
			return e
		}
		return audit.MutationTx(ctx, tx, r.clock.Now(), "git_profile", "update", contract.AuditTarget{Type: "installation", ID: installationID})
	})
	return result, r.mapMutationError(err)
}
