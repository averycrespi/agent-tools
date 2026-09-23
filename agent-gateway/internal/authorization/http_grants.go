package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

type HTTPGrantInput struct {
	PrincipalID string
	Description *string
	Policy      json.RawMessage
	ExpiresAt   *time.Time
}

const httpGrantSelect = `SELECT id,principal_id,description,revision,policy_json,expires_at,created_at,updated_at FROM http_grants`

func scanHTTPGrant(row interface{ Scan(...any) error }, now time.Time) (contract.HTTPGrant, httppolicy.Policy, error) {
	var g contract.HTTPGrant
	var description, expires sql.NullString
	var raw string
	if err := row.Scan(&g.ID, &g.PrincipalID, &description, &g.Revision, &raw, &expires, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return g, httppolicy.Policy{}, err
	}
	if description.Valid {
		g.Description = &description.String
	}
	if expires.Valid {
		g.ExpiresAt = &expires.String
	}
	p, err := httppolicy.DecodePolicy([]byte(raw))
	created, createdOK := canonicalTimestamp(g.CreatedAt)
	updated, updatedOK := canonicalTimestamp(g.UpdatedAt)
	canonical, encodeErr := p.JSON()
	if err != nil || encodeErr != nil || string(canonical) != raw || !validOpaqueID(g.ID) || !validOpaqueID(g.PrincipalID) || !validRevision(g.Revision) || !validGrantDescription(g.Description) || !createdOK || !updatedOK || updated.Before(created) {
		return g, p, ErrInvalidState
	}
	g.Policy = json.RawMessage(raw)
	g.State = contract.GrantActive
	if expires.Valid {
		expiry, ok := canonicalTimestamp(expires.String)
		if !ok || !expiry.After(created) {
			return g, p, ErrInvalidState
		}
		if !expiry.After(now) {
			g.State = contract.GrantExpired
		}
	}
	return g, p, nil
}

func httpGrantTx(ctx context.Context, tx *sql.Tx, id string, now time.Time) (contract.HTTPGrant, httppolicy.Policy, error) {
	return scanHTTPGrant(tx.QueryRowContext(ctx, httpGrantSelect+` WHERE id=?`, id), now)
}

func (r *Repository) GetHTTPGrant(ctx context.Context, id string) (g contract.HTTPGrant, err error) {
	if !validOpaqueID(id) {
		return g, ErrNotFound
	}
	err = r.view(ctx, func(tx *sql.Tx) error { var e error; g, _, e = httpGrantTx(ctx, tx, id, r.clock.Now()); return e })
	return
}

func validateHTTPInput(in HTTPGrantInput, now time.Time) (httppolicy.Policy, []byte, error) {
	if !validOpaqueID(in.PrincipalID) || !validGrantDescription(in.Description) || in.ExpiresAt != nil && !in.ExpiresAt.After(now) {
		return httppolicy.Policy{}, nil, ErrInvalidInput
	}
	p, err := httppolicy.DecodePolicy(in.Policy)
	if err != nil {
		return p, nil, ErrInvalidInput
	}
	raw, err := p.JSON()
	return p, raw, err
}

func checkHTTPReferenceTx(ctx context.Context, tx *sql.Tx, p httppolicy.Policy) error {
	if p.CredentialID() == "" {
		return nil
	}
	err := httpcredentials.CheckPolicyReferenceTx(ctx, tx, p.CredentialID(), p)
	if errors.Is(err, httpcredentials.ErrNotFound) || errors.Is(err, httpcredentials.ErrReferenced) || errors.Is(err, httpcredentials.ErrInvalid) {
		return ErrConflict
	}
	return err
}

// PutHTTPGrant creates when id is empty, otherwise atomically replaces the full
// policy at the exact resource revision. Identity and principal remain stable.
func (r *Repository) PutHTTPGrant(ctx context.Context, id, revision string, in HTTPGrantInput) (contract.HTTPGrant, error) {
	now := r.clock.Now().UTC()
	p, raw, err := validateHTTPInput(in, now)
	if err != nil {
		return contract.HTTPGrant{}, err
	}
	create := id == ""
	if create {
		id, err = r.newID(now)
	} else if !validOpaqueID(id) || !validRevision(revision) {
		err = ErrInvalidInput
	}
	if err != nil {
		return contract.HTTPGrant{}, err
	}
	var result contract.HTTPGrant
	err = r.mutateAuthorityTx(ctx, "", func(tx *sql.Tx) error {
		if _, err := principalByIDTx(ctx, tx, in.PrincipalID); err != nil {
			return err
		}
		if err := checkHTTPReferenceTx(ctx, tx, p); err != nil {
			return err
		}
		action := "update"
		if create {
			action = "create"
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM http_grants`).Scan(&count); err != nil {
				return err
			}
			if count >= contract.HTTPPolicyGrants {
				return ErrResourceLimit
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO http_grants(id,principal_id,description,revision,policy_json,expires_at,created_at,updated_at) VALUES(?,?,?,1,?,?,?,?)`, id, in.PrincipalID, nullableGrantString(in.Description), string(raw), nullableGrantTime(in.ExpiresAt), formatAuthorizationTime(now), formatAuthorizationTime(now))
			if err != nil {
				return err
			}
		} else {
			current, _, err := httpGrantTx(ctx, tx, id, now)
			if err != nil {
				return err
			}
			if current.Revision != revision {
				return ErrStaleRevision
			}
			if current.PrincipalID != in.PrincipalID {
				return ErrConflict
			}
			if _, err := tx.ExecContext(ctx, `UPDATE http_grants SET description=?,policy_json=?,expires_at=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, nullableGrantString(in.Description), string(raw), nullableGrantTime(in.ExpiresAt), formatAuthorizationTime(now), id, revision); err != nil {
				return err
			}
		}
		if err := advanceAuthorizationRevisionTx(ctx, tx); err != nil {
			return err
		}
		var err error
		result, _, err = httpGrantTx(ctx, tx, id, now)
		if err != nil {
			return err
		}
		return audit.MutationTx(ctx, tx, now, "http_grant", action, contract.AuditTarget{Type: "http_grant", ID: id})
	})
	return result, r.mapMutationError(err)
}

func (r *Repository) DeleteHTTPGrant(ctx context.Context, id, revision string) error {
	if !validOpaqueID(id) || !validRevision(revision) {
		return ErrInvalidInput
	}
	err := r.mutateAuthorityTx(ctx, "", func(tx *sql.Tx) error {
		current, _, err := httpGrantTx(ctx, tx, id, r.clock.Now())
		if err != nil {
			return err
		}
		if current.Revision != revision {
			return ErrStaleRevision
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM http_grants WHERE id=? AND revision=?`, id, revision); err != nil {
			return err
		}
		if err := advanceAuthorizationRevisionTx(ctx, tx); err != nil {
			return err
		}
		return audit.MutationTx(ctx, tx, r.clock.Now(), "http_grant", "delete", contract.AuditTarget{Type: "http_grant", ID: id})
	})
	return r.mapMutationError(err)
}

type principalHTTPDefault struct {
	Default  contract.HTTPDefault
	Revision string
}

func httpDefaultTx(ctx context.Context, tx *sql.Tx, id string) (principalHTTPDefault, error) {
	d := principalHTTPDefault{}
	err := tx.QueryRowContext(ctx, `SELECT policy,revision FROM http_defaults WHERE principal_id=?`, id).Scan(&d.Default, &d.Revision)
	if err == nil && (d.Default != contract.HTTPDefaultAllow && d.Default != contract.HTTPDefaultBlock || !validRevision(d.Revision)) {
		err = ErrInvalidState
	}
	return d, err
}

// ReferencesTx deliberately retains expired references: metadata edits cannot
// make retained configuration incompatible merely because it is inactive today.
func (r *Repository) ReferencesTx(ctx context.Context, tx *sql.Tx, id string) ([]httpcredentials.Reference, error) {
	grants, err := readHTTPGrantsTx(ctx, tx, r.clock.Now())
	if err != nil {
		return nil, err
	}
	out := []httpcredentials.Reference{}
	for _, g := range grants {
		if g.policy.CredentialID() == id {
			out = append(out, httpcredentials.Reference{ID: g.resource.ID, Policy: g.policy})
		}
	}
	return out, nil
}

type storedHTTPGrant struct {
	resource contract.HTTPGrant
	policy   httppolicy.Policy
}

func readHTTPGrantsTx(ctx context.Context, tx *sql.Tx, now time.Time) ([]storedHTTPGrant, error) {
	rows, err := tx.QueryContext(ctx, httpGrantSelect+` ORDER BY id LIMIT ?`, contract.HTTPPolicyGrants+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []storedHTTPGrant{}
	for rows.Next() {
		g, p, err := scanHTTPGrant(rows, now)
		if err != nil {
			return nil, err
		}
		if len(out) >= contract.HTTPPolicyGrants {
			return nil, ErrInvalidState
		}
		out = append(out, storedHTTPGrant{g, p})
	}
	return out, rows.Err()
}

func validateHTTPAuthorityTx(ctx context.Context, tx *sql.Tx, principals map[string]struct{}) error {
	rows, err := tx.QueryContext(ctx, `SELECT principal_id,policy,revision FROM http_defaults`)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var id, policy, revision string
		if err := rows.Scan(&id, &policy, &revision); err != nil {
			_ = rows.Close()
			return err
		}
		_, exists := principals[id]
		if !exists || seen[id] || !validRevision(revision) || policy != "allow" && policy != "block" {
			_ = rows.Close()
			return ErrInvalidState
		}
		seen[id] = true
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	if len(seen) != len(principals) {
		return ErrInvalidState
	}
	grants, err := readHTTPGrantsTx(ctx, tx, time.Now())
	if err != nil {
		return err
	}
	for _, g := range grants {
		if _, exists := principals[g.resource.PrincipalID]; !exists {
			return ErrInvalidState
		}
		if err := checkHTTPReferenceTx(ctx, tx, g.policy); err != nil {
			return ErrInvalidState
		}
	}
	return nil
}

func httpRevision(value string) (uint64, error) {
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || n == 0 {
		return 0, ErrInvalidState
	}
	return n, nil
}
