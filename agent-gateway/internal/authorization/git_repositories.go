package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

const gitRepositorySelect = `SELECT id,name,url,aliases_json,credential_id,revision,alias_revision,deleted,created_at,updated_at FROM git_repositories`

type storedGitRepository struct {
	resource contract.GitRepository
	deleted  bool
}

func normalizeGitRepository(def contract.GitRepositoryDefinition) (contract.GitRepositoryDefinition, error) {
	if !validGrantDescription(&def.Name) || def.CredentialID != nil && !validOpaqueID(*def.CredentialID) {
		return def, ErrInvalidInput
	}
	canonical, err := gitpolicy.Locator(def.URL)
	if err != nil {
		return def, ErrInvalidInput
	}
	aliases, err := gitpolicy.Aliases(canonical, def.Aliases)
	if err != nil {
		return def, ErrInvalidInput
	}
	def.URL, def.Aliases = canonical, aliases
	return def, nil
}

func scanGitRepository(row interface{ Scan(...any) error }) (storedGitRepository, error) {
	var rec storedGitRepository
	g := &rec.resource
	var raw string
	var credential sql.NullString
	if err := row.Scan(&g.ID, &g.Name, &g.URL, &raw, &credential, &g.Revision, &g.AliasRevision, &rec.deleted, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return rec, err
	}
	if strictjson.Decode([]byte(raw), &g.Aliases, strictjson.Options{MaxBytes: contract.GitPolicyBytes, MaxDepth: 2}) != nil {
		return rec, ErrInvalidState
	}
	if credential.Valid {
		g.CredentialID = &credential.String
	}
	canonical, err := normalizeGitRepository(g.GitRepositoryDefinition)
	encoded, _ := json.Marshal(canonical.Aliases)
	created, createdOK := canonicalTimestamp(g.CreatedAt)
	updated, updatedOK := canonicalTimestamp(g.UpdatedAt)
	if err != nil || canonical.URL != g.URL || string(encoded) != raw || !validOpaqueID(g.ID) || !validRevision(g.Revision) || !validRevision(g.AliasRevision) || !createdOK || !updatedOK || updated.Before(created) {
		return rec, ErrInvalidState
	}
	return rec, nil
}

func gitRepositoryTx(ctx context.Context, tx *sql.Tx, id string) (storedGitRepository, error) {
	return scanGitRepository(tx.QueryRowContext(ctx, gitRepositorySelect+` WHERE id=?`, id))
}

func readGitRepositoriesTx(ctx context.Context, tx *sql.Tx) ([]storedGitRepository, error) {
	rows, err := tx.QueryContext(ctx, gitRepositorySelect+` ORDER BY id LIMIT ?`, contract.GitRepositoryIdentities+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []storedGitRepository{}
	active := 0
	for rows.Next() {
		rec, err := scanGitRepository(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
		if !rec.deleted {
			active++
		}
	}
	if len(out) > contract.GitRepositoryIdentities || active > contract.GitRepositories {
		return nil, ErrInvalidState
	}
	return out, rows.Err()
}

func gitOrigin(locator string) string { u, _ := url.Parse(locator); return "https://" + u.Host }

func checkGitCredentialTx(ctx context.Context, tx *sql.Tx, def contract.GitRepositoryDefinition) error {
	if def.CredentialID == nil {
		return nil
	}
	var origin string
	var deleted bool
	if err := tx.QueryRowContext(ctx, `SELECT origin,deleted FROM git_credentials WHERE id=?`, *def.CredentialID).Scan(&origin, &deleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	if deleted || origin != gitOrigin(def.URL) {
		return ErrConflict
	}
	return nil
}

func checkGitLocators(repositories []storedGitRepository, id string, def contract.GitRepositoryDefinition) error {
	values := append([]string{def.URL}, def.Aliases...)
	for _, rec := range repositories {
		if rec.resource.ID == id {
			continue
		}
		others := append([]string{rec.resource.URL}, rec.resource.Aliases...)
		for _, a := range values {
			for _, b := range others {
				if gitpolicy.Overlaps(a, b) || stringsGitBase(a) == stringsGitBase(b) {
					return ErrConflict
				}
			}
		}
	}
	return nil
}

func stringsGitBase(raw string) string {
	if len(raw) > 4 && raw[len(raw)-4:] == ".git" {
		return raw[:len(raw)-4]
	}
	return raw
}

func (r *Repository) GetGitRepository(ctx context.Context, id string) (g contract.GitRepository, err error) {
	if !validOpaqueID(id) {
		return g, ErrNotFound
	}
	err = r.view(ctx, func(tx *sql.Tx) error {
		rec, e := gitRepositoryTx(ctx, tx, id)
		if e != nil {
			return e
		}
		if rec.deleted {
			return ErrNotFound
		}
		g = rec.resource
		return nil
	})
	return
}

func (r *Repository) ListGitRepositories(ctx context.Context) ([]contract.GitRepository, error) {
	out := []contract.GitRepository{}
	err := r.view(ctx, func(tx *sql.Tx) error {
		rows, e := readGitRepositoriesTx(ctx, tx)
		if e != nil {
			return e
		}
		for _, rec := range rows {
			if !rec.deleted {
				out = append(out, rec.resource)
			}
		}
		return nil
	})
	return out, err
}

func (r *Repository) PutGitRepository(ctx context.Context, id, revision string, def contract.GitRepositoryDefinition) (contract.GitRepository, error) {
	def, err := normalizeGitRepository(def)
	if err != nil {
		return contract.GitRepository{}, err
	}
	create := id == ""
	now := r.clock.Now().UTC()
	if create {
		id, err = r.newID(now)
	} else if !validOpaqueID(id) || !validRevision(revision) {
		err = ErrInvalidInput
	}
	if err != nil {
		return contract.GitRepository{}, err
	}
	var result contract.GitRepository
	err = r.mutateAuthorityTx(ctx, "", func(tx *sql.Tx) error {
		rows, e := readGitRepositoriesTx(ctx, tx)
		if e != nil {
			return e
		}
		if e := checkGitCredentialTx(ctx, tx, def); e != nil {
			return e
		}
		if e := checkGitLocators(rows, id, def); e != nil {
			return e
		}
		aliases, _ := json.Marshal(def.Aliases)
		action := "update"
		if create {
			action = "create"
			active := 0
			for _, rec := range rows {
				if !rec.deleted {
					active++
				}
			}
			if len(rows) >= contract.GitRepositoryIdentities || active >= contract.GitRepositories {
				return ErrResourceLimit
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO git_repositories(id,name,url,aliases_json,credential_id,revision,alias_revision,deleted,created_at,updated_at) VALUES(?,?,?,?,?,1,1,0,?,?)`, id, def.Name, def.URL, string(aliases), nullableGrantString(def.CredentialID), formatAuthorizationTime(now), formatAuthorizationTime(now))
		} else {
			current, e := gitRepositoryTx(ctx, tx, id)
			if e != nil {
				return e
			}
			if current.deleted {
				return ErrNotFound
			}
			if current.resource.Revision != revision {
				return ErrStaleRevision
			}
			if current.resource.URL != def.URL {
				return ErrConflict
			}
			aliasRevision, e := strconv.ParseInt(current.resource.AliasRevision, 10, 64)
			if e != nil {
				return ErrInvalidState
			}
			if !slices.Equal(current.resource.Aliases, def.Aliases) {
				aliasRevision++
			}
			_, e = tx.ExecContext(ctx, `UPDATE git_repositories SET name=?,aliases_json=?,credential_id=?,revision=revision+1,alias_revision=?,updated_at=? WHERE id=? AND revision=?`, def.Name, string(aliases), nullableGrantString(def.CredentialID), aliasRevision, formatAuthorizationTime(now), id, revision)
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
		rec, e := gitRepositoryTx(ctx, tx, id)
		if e != nil {
			return e
		}
		result = rec.resource
		return audit.MutationTx(ctx, tx, now, "git_repository", action, contract.AuditTarget{Type: "git_repository", ID: id})
	})
	return result, r.mapMutationError(err)
}

func (r *Repository) DeleteGitRepository(ctx context.Context, id, revision string) error {
	if !validOpaqueID(id) || !validRevision(revision) {
		return ErrInvalidInput
	}
	err := r.mutateAuthorityTx(ctx, "", func(tx *sql.Tx) error {
		rec, e := gitRepositoryTx(ctx, tx, id)
		if e != nil {
			return e
		}
		if rec.deleted {
			return ErrNotFound
		}
		if rec.resource.Revision != revision {
			return ErrStaleRevision
		}
		if _, e := tx.ExecContext(ctx, `UPDATE git_repositories SET deleted=1,revision=revision+1,updated_at=? WHERE id=?`, formatAuthorizationTime(r.clock.Now()), id); e != nil {
			return e
		}
		if e := advanceAuthorizationRevisionTx(ctx, tx); e != nil {
			return e
		}
		return audit.MutationTx(ctx, tx, r.clock.Now(), "git_repository", "delete", contract.AuditTarget{Type: "git_repository", ID: id})
	})
	return r.mapMutationError(err)
}

// GitCredentialReferenceIDsTx reads the bounded reference inventory once for a
// credential collection, including permanent tombstones without hydrating policies.
func GitCredentialReferenceIDsTx(ctx context.Context, tx *sql.Tx) (map[string][]contract.GitCredentialReference, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,credential_id FROM git_repositories WHERE credential_id IS NOT NULL ORDER BY id LIMIT ?`, contract.GitRepositoryIdentities+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string][]contract.GitCredentialReference)
	count := 0
	for rows.Next() {
		var id, credential string
		if err := rows.Scan(&id, &credential); err != nil {
			return nil, err
		}
		count++
		out[credential] = append(out[credential], contract.GitCredentialReference{ID: id})
	}
	if count > contract.GitRepositoryIdentities {
		return nil, ErrInvalidState
	}
	return out, rows.Err()
}

// GitCredentialReferencesTx includes tombstoned repository configuration. The
// credential owner calls it on its supplied writer, never through a nested view.
func GitCredentialReferencesTx(ctx context.Context, tx *sql.Tx, id string) ([]contract.GitRepository, error) {
	rows, err := readGitRepositoriesTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := []contract.GitRepository{}
	for _, rec := range rows {
		if rec.resource.CredentialID != nil && *rec.resource.CredentialID == id {
			out = append(out, rec.resource)
		}
	}
	return out, nil
}
