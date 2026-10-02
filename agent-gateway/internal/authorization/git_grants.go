package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
)

type GitGrantInput struct {
	PrincipalID  string
	RepositoryID string
	Description  *string
	Policy       json.RawMessage
	ExpiresAt    *time.Time
}

const gitGrantSelect = `SELECT id,principal_id,repository_id,description,revision,policy_json,expires_at,created_at,updated_at FROM git_grants`

func scanGitGrant(row interface{ Scan(...any) error }, now time.Time) (contract.GitGrant, error) {
	var g contract.GitGrant
	var description, expiry sql.NullString
	var raw string
	if err := row.Scan(&g.ID, &g.PrincipalID, &g.RepositoryID, &description, &g.Revision, &raw, &expiry, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return g, err
	}
	if description.Valid {
		g.Description = &description.String
	}
	if expiry.Valid {
		g.ExpiresAt = &expiry.String
	}
	p, err := gitpolicy.Decode([]byte(raw))
	canonical, encodeErr := gitpolicy.JSON(p)
	created, createdOK := canonicalTimestamp(g.CreatedAt)
	updated, updatedOK := canonicalTimestamp(g.UpdatedAt)
	if err != nil || encodeErr != nil || string(canonical) != raw || !validOpaqueID(g.ID) || !validOpaqueID(g.PrincipalID) || !validOpaqueID(g.RepositoryID) || !validRevision(g.Revision) || !validGrantDescription(g.Description) || !createdOK || !updatedOK || updated.Before(created) {
		return g, ErrInvalidState
	}
	g.Policy = p
	g.State = contract.GrantActive
	if expiry.Valid {
		expires, ok := canonicalTimestamp(expiry.String)
		if !ok || !expires.After(created) {
			return g, ErrInvalidState
		}
		if !expires.After(now) {
			g.State = contract.GrantExpired
		}
	}
	return g, nil
}

func gitGrantTx(ctx context.Context, tx *sql.Tx, id string, now time.Time) (contract.GitGrant, error) {
	return scanGitGrant(tx.QueryRowContext(ctx, gitGrantSelect+` WHERE id=?`, id), now)
}

func readGitGrantsTx(ctx context.Context, tx *sql.Tx, now time.Time) ([]contract.GitGrant, error) {
	rows, err := tx.QueryContext(ctx, gitGrantSelect+` ORDER BY id LIMIT ?`, contract.GitGrants+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []contract.GitGrant{}
	for rows.Next() {
		g, e := scanGitGrant(rows, now)
		if e != nil {
			return nil, e
		}
		out = append(out, g)
	}
	if len(out) > contract.GitGrants {
		return nil, ErrInvalidState
	}
	return out, rows.Err()
}

func (r *Repository) GetGitGrant(ctx context.Context, id string) (g contract.GitGrant, err error) {
	if !validOpaqueID(id) {
		return g, ErrNotFound
	}
	err = r.view(ctx, func(tx *sql.Tx) error { var e error; g, e = gitGrantTx(ctx, tx, id, r.clock.Now()); return e })
	return
}
func (r *Repository) ListGitGrants(ctx context.Context) (out []contract.GitGrant, err error) {
	err = r.view(ctx, func(tx *sql.Tx) error { var e error; out, e = readGitGrantsTx(ctx, tx, r.clock.Now()); return e })
	return
}

func (r *Repository) PutGitGrant(ctx context.Context, id, revision string, in GitGrantInput) (contract.GitGrant, error) {
	now := r.clock.Now().UTC()
	if !validOpaqueID(in.PrincipalID) || !validOpaqueID(in.RepositoryID) || !validGrantDescription(in.Description) || in.ExpiresAt != nil && !in.ExpiresAt.After(now) {
		return contract.GitGrant{}, ErrInvalidInput
	}
	p, err := gitpolicy.Decode(in.Policy)
	if err != nil {
		return contract.GitGrant{}, ErrInvalidInput
	}
	raw, err := gitpolicy.JSON(p)
	if err != nil {
		return contract.GitGrant{}, ErrInvalidInput
	}
	create := id == ""
	if create {
		id, err = r.newID(now)
	} else if !validOpaqueID(id) || !validRevision(revision) {
		err = ErrInvalidInput
	}
	if err != nil {
		return contract.GitGrant{}, err
	}
	var result contract.GitGrant
	err = r.mutateAuthorityTx(ctx, "", func(tx *sql.Tx) error {
		if _, e := principalByIDTx(ctx, tx, in.PrincipalID); e != nil {
			return e
		}
		repo, e := gitRepositoryTx(ctx, tx, in.RepositoryID)
		if e != nil {
			return e
		}
		if repo.deleted {
			return ErrConflict
		}
		action := "update"
		if create {
			action = "create"
			var count int
			if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM git_grants`).Scan(&count); e != nil {
				return e
			}
			if count >= contract.GitGrants {
				return ErrResourceLimit
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO git_grants(id,principal_id,repository_id,description,revision,policy_json,expires_at,created_at,updated_at) VALUES(?,?,?,?,1,?,?,?,?)`, id, in.PrincipalID, in.RepositoryID, nullableGrantString(in.Description), string(raw), nullableGrantTime(in.ExpiresAt), formatAuthorizationTime(now), formatAuthorizationTime(now))
		} else {
			current, e := gitGrantTx(ctx, tx, id, now)
			if e != nil {
				return e
			}
			if current.Revision != revision {
				return ErrStaleRevision
			}
			if current.PrincipalID != in.PrincipalID || current.RepositoryID != in.RepositoryID {
				return ErrConflict
			}
			_, e = tx.ExecContext(ctx, `UPDATE git_grants SET description=?,policy_json=?,expires_at=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, nullableGrantString(in.Description), string(raw), nullableGrantTime(in.ExpiresAt), formatAuthorizationTime(now), id, revision)
			if e != nil {
				return e
			}
		}
		if e != nil {
			return e
		}
		if e := advanceAuthorizationRevisionTx(ctx, tx); e != nil {
			return e
		}
		result, e = gitGrantTx(ctx, tx, id, now)
		if e != nil {
			return e
		}
		return audit.MutationTx(ctx, tx, now, "git_grant", action, contract.AuditTarget{Type: "git_grant", ID: id})
	})
	return result, r.mapMutationError(err)
}

func (r *Repository) DeleteGitGrant(ctx context.Context, id, revision string) error {
	if !validOpaqueID(id) || !validRevision(revision) {
		return ErrInvalidInput
	}
	err := r.mutateAuthorityTx(ctx, "", func(tx *sql.Tx) error {
		current, e := gitGrantTx(ctx, tx, id, r.clock.Now())
		if e != nil {
			return e
		}
		if current.Revision != revision {
			return ErrStaleRevision
		}
		if _, e := tx.ExecContext(ctx, `DELETE FROM git_grants WHERE id=?`, id); e != nil {
			return e
		}
		if e := advanceAuthorizationRevisionTx(ctx, tx); e != nil {
			return e
		}
		return audit.MutationTx(ctx, tx, r.clock.Now(), "git_grant", "delete", contract.AuditTarget{Type: "git_grant", ID: id})
	})
	return r.mapMutationError(err)
}

// EvaluateGit is policy-only. Authentication, profile activation, immutable wire
// binding, material acquisition and durable admission remain later owners.
func (r *Repository) EvaluateGit(ctx context.Context, principalID, repositoryID string, actions []gitpolicy.RefAction) (contract.GitDecision, error) {
	var decision contract.GitDecision
	if !validOpaqueID(principalID) || !validOpaqueID(repositoryID) {
		return decision, ErrInvalidInput
	}
	err := r.view(ctx, func(tx *sql.Tx) error {
		principal, e := principalByIDTx(ctx, tx, principalID)
		if e != nil {
			return e
		}
		repo, e := gitRepositoryTx(ctx, tx, repositoryID)
		if e != nil {
			return e
		}
		decision = contract.GitDecision{Principal: contract.GitRevisionRef{ID: principalID, Revision: principal.Revision}, Repository: contract.GitRevisionRef{ID: repositoryID, Revision: repo.resource.Revision}, Reason: "default_deny"}
		if e := tx.QueryRowContext(ctx, `SELECT revision + 1 FROM authorization_meta WHERE singleton=1`).Scan(&decision.AuthorizationRevision); e != nil {
			return e
		}
		grants, e := readGitGrantsTx(ctx, tx, r.clock.Now())
		if e != nil {
			return e
		}
		applicable := []gitpolicy.Grant{}
		for _, g := range grants {
			if g.PrincipalID == principalID && g.RepositoryID == repositoryID && g.State == contract.GrantActive {
				applicable = append(applicable, gitpolicy.Grant{Policy: g.Policy})
			}
		}
		allowed, e := gitpolicy.Evaluate(applicable, actions)
		if e != nil {
			return ErrInvalidInput
		}
		if principal.State != contract.PrincipalActive || repo.deleted {
			allowed = false
		}
		decision.Allowed = allowed
		if allowed {
			decision.Reason = "allow"
		}
		return nil
	})
	return decision, err
}

// ValidateGitBackupTx validates Git's complete retained graph on a read-only
// artifact transaction. It neither migrates nor constructs a live authority owner.
func ValidateGitBackupTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM principals ORDER BY id LIMIT ?`, mustLimit("principals")+1)
	if err != nil {
		return err
	}
	principals := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if !validOpaqueID(id) {
			_ = rows.Close()
			return ErrInvalidState
		}
		principals[id] = struct{}{}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	if int64(len(principals)) > mustLimit("principals") {
		return ErrInvalidState
	}
	return validateGitAuthorityTx(ctx, tx, principals)
}

func validateGitAuthorityTx(ctx context.Context, tx *sql.Tx, principals map[string]struct{}) error {
	repositories, err := readGitRepositoriesTx(ctx, tx)
	if err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, rec := range repositories {
		ids[rec.resource.ID] = true
		if err := checkGitLocators(repositories, rec.resource.ID, rec.resource.GitRepositoryDefinition); err != nil {
			return ErrInvalidState
		}
		if err := checkGitCredentialTx(ctx, tx, rec.resource.GitRepositoryDefinition); err != nil {
			return ErrInvalidState
		}
	}
	grants, err := readGitGrantsTx(ctx, tx, time.Now())
	if err != nil {
		return err
	}
	for _, g := range grants {
		if _, ok := principals[g.PrincipalID]; !ok {
			return ErrInvalidState
		}
		if !ids[g.RepositoryID] {
			return ErrInvalidState
		}
	}
	_, err = gitRoutingProfileTx(ctx, tx)
	return err
}
