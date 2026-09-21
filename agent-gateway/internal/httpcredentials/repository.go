package httpcredentials

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

var (
	ErrNotFound    = errors.New("HTTP credential not found")
	ErrStale       = errors.New("HTTP credential revision is stale")
	ErrReferenced  = errors.New("HTTP credential is referenced")
	ErrLimit       = errors.New("HTTP credential limit reached")
	ErrUnavailable = errors.New("HTTP credential unavailable")
)

const identityLimit = 1024

type Clock interface{ Now() time.Time }

// ReferenceInspector must inspect every referencing grant on the supplied
// transaction. The grant owner must validate creation/update on that same
// transaction through CheckReferenceTx. Neither seam may acquire storage again.
type ReferenceInspector interface {
	ReferencesTx(context.Context, *sql.Tx, string) ([]Reference, error)
}

type Reference struct {
	ID     string            `json:"id"`
	Policy httppolicy.Policy `json:"-"`
}

type Resource struct {
	ID string `json:"id"`
	Definition
	Revision   string      `json:"revision"`
	Available  bool        `json:"available"`
	References []Reference `json:"referencing_grants"`
	CreatedAt  string      `json:"created_at"`
	UpdatedAt  string      `json:"updated_at"`
}

type record struct {
	Resource
	materialRevision uint64
	handle           sql.NullString
	deleted          bool
}

type Repository struct {
	store      *storage.Store
	clock      Clock
	entropy    io.Reader
	references ReferenceInspector
}

func NewRepository(store *storage.Store, clock Clock, entropy io.Reader, refs ReferenceInspector) (*Repository, error) {
	if store == nil || clock == nil || entropy == nil || refs == nil {
		return nil, ErrInvalid
	}
	return &Repository{store: store, clock: clock, entropy: entropy, references: refs}, nil
}

func readTx(ctx context.Context, tx *sql.Tx, id string) (record, error) {
	var r record
	var revision uint64
	err := tx.QueryRowContext(ctx, `SELECT id,name,host,port,allow_wildcard,header,prefix,revision,material_revision,handle,deleted,created_at,updated_at FROM http_credentials WHERE id=?`, id).Scan(&r.ID, &r.Name, &r.Boundary.Host, &r.Boundary.Port, &r.Boundary.AllowWildcard, &r.Recipe.Header, &r.Recipe.Prefix, &revision, &r.materialRevision, &r.handle, &r.deleted, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return record{}, ErrNotFound
	}
	if err != nil {
		return record{}, err
	}
	r.Revision = strconv.FormatUint(revision, 10)
	r.References = []Reference{}
	return r, nil
}

func (r *Repository) readReferences(ctx context.Context, tx *sql.Tx, rec *record) error {
	refs, err := r.references.ReferencesTx(ctx, tx, rec.ID)
	if err != nil {
		return err
	}
	if len(refs) > contract.HTTPPolicyGrants {
		return ErrInvalid
	}
	seen := make(map[string]bool)
	for _, ref := range refs {
		if !contract.ValidAuditID(ref.ID) || seen[ref.ID] || ref.Policy.CredentialID() != rec.ID {
			return ErrInvalid
		}
		seen[ref.ID] = true
	}
	rec.References = append([]Reference{}, refs...)
	return nil
}

func (r *Repository) Get(ctx context.Context, id string) (Resource, error) {
	var rec record
	err := r.store.View(ctx, func(tx *sql.Tx) error {
		var err error
		rec, err = readTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if rec.deleted {
			return ErrNotFound
		}
		if err := r.readReferences(ctx, tx, &rec); err != nil {
			return err
		}
		return availabilityTx(ctx, tx, &rec)
	})
	if err != nil {
		return Resource{}, err
	}
	if r.store.Latched() {
		rec.Available = false
	}
	return rec.Resource, nil
}

func availabilityTx(ctx context.Context, tx *sql.Tx, rec *record) error {
	var current int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM keyring_authorities a WHERE a.owner=? AND a.kind='http_credential' AND a.handle=? AND a.revision=? AND NOT EXISTS (SELECT 1 FROM keyring_authority_fences f WHERE f.owner=a.owner AND f.kind=a.kind)`, rec.ID, rec.handle, rec.materialRevision).Scan(&current)
	rec.Available = !rec.deleted && rec.handle.Valid && current == 1
	return err
}

func (r *Repository) List(ctx context.Context) ([]Resource, error) {
	out := []Resource{}
	err := r.store.View(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM http_credentials WHERE deleted=0 ORDER BY insertion_sequence DESC LIMIT ?`, contract.HTTPPolicyCredentials+1)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		if len(ids) > contract.HTTPPolicyCredentials {
			return ErrInvalid
		}
		for _, id := range ids {
			rec, err := readTx(ctx, tx, id)
			if err != nil {
				return err
			}
			if err := r.readReferences(ctx, tx, &rec); err != nil {
				return err
			}
			if err := availabilityTx(ctx, tx, &rec); err != nil {
				return err
			}
			if r.store.Latched() {
				rec.Available = false
			}
			out = append(out, rec.Resource)
		}
		return nil
	})
	return out, err
}

func (r *Repository) create(ctx context.Context, def Definition) (Resource, error) {
	def, err := Normalize(def)
	if err != nil {
		return Resource{}, err
	}
	var resource Resource
	err = r.store.Mutate(ctx, func(tx *sql.Tx) error {
		var total, active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(deleted=0),0) FROM http_credentials`).Scan(&total, &active); err != nil {
			return err
		}
		if total >= identityLimit || active >= contract.HTTPPolicyCredentials {
			return ErrLimit
		}
		id, err := r.newID(r.clock.Now())
		if err != nil {
			return ErrUnavailable
		}
		now := r.clock.Now().UTC().Format(time.RFC3339Nano)
		resource = Resource{ID: id, Definition: def, Revision: "1", References: []Reference{}, CreatedAt: now, UpdatedAt: now}
		_, err = tx.ExecContext(ctx, `INSERT INTO http_credentials(id,name,host,port,allow_wildcard,header,prefix,revision,material_revision,deleted,created_at,updated_at) VALUES(?,?,?,?,?,?,?,1,0,0,?,?)`, resource.ID, def.Name, def.Boundary.Host, def.Boundary.Port, def.Boundary.AllowWildcard, def.Recipe.Header, def.Recipe.Prefix, now, now)
		if err != nil {
			return err
		}
		return r.auditTx(ctx, tx, resource.ID, "create")
	})
	if err != nil {
		return Resource{}, err
	}
	return resource, nil
}

func (r *Repository) update(ctx context.Context, id, revision string, def Definition) (Resource, error) {
	def, err := Normalize(def)
	if err != nil {
		return Resource{}, err
	}
	err = r.store.Mutate(ctx, func(tx *sql.Tx) error {
		rec, err := readTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := precondition(rec, revision); err != nil {
			return err
		}
		if err := r.readReferences(ctx, tx, &rec); err != nil {
			return err
		}
		ref, err := revisionRef(rec)
		if err != nil {
			return err
		}
		for _, reference := range rec.References {
			contains, err := httppolicy.CredentialContains(def.PolicyCredential(ref, true), reference.Policy)
			if err != nil || !contains {
				return ErrReferenced
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE http_credentials SET name=?,host=?,port=?,allow_wildcard=?,header=?,prefix=?,revision=revision+1,updated_at=? WHERE id=?`, def.Name, def.Boundary.Host, def.Boundary.Port, def.Boundary.AllowWildcard, def.Recipe.Header, def.Recipe.Prefix, r.clock.Now().UTC().Format(time.RFC3339Nano), id)
		if err != nil {
			return err
		}
		return r.auditTx(ctx, tx, id, "update")
	})
	if err != nil {
		return Resource{}, err
	}
	return r.Get(ctx, id)
}

func precondition(rec record, revision string) error {
	if rec.deleted {
		return ErrNotFound
	}
	if revision == "" || rec.Revision != revision {
		return ErrStale
	}
	return nil
}
func revisionRef(rec record) (contract.HTTPRevisionRef, error) {
	rev, err := strconv.ParseUint(rec.Revision, 10, 64)
	if err != nil || rev == 0 {
		return contract.HTTPRevisionRef{}, ErrInvalid
	}
	return contract.HTTPRevisionRef{ID: rec.ID, Revision: rev}, nil
}

// CheckReferenceTx is the grant owner's transactional insertion/update seam.
// Holding the same control writer excludes incompatible edits and deletion.
func (r *Repository) CheckReferenceTx(ctx context.Context, tx *sql.Tx, id string, policy httppolicy.Policy) error {
	if policy.CredentialID() != id {
		return ErrReferenced
	}
	rec, err := readTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if rec.deleted {
		return ErrNotFound
	}
	ref, err := revisionRef(rec)
	if err != nil {
		return err
	}
	contained, err := httppolicy.CredentialContains(rec.PolicyCredential(ref, true), policy)
	if err != nil || !contained {
		return ErrReferenced
	}
	return nil
}

func (r *Repository) auditTx(ctx context.Context, tx *sql.Tx, id, action string) error {
	return audit.MutationTx(ctx, tx, r.clock.Now(), "http_credential", action, contract.AuditTarget{Type: "http_credential", ID: id})
}

func (r *Repository) callback(id, revision string, remove bool) keyring.AuthorityCallback {
	return func(ctx context.Context, tx *sql.Tx, update keyring.AuthorityUpdate) (string, error) {
		if update.Owner != id || update.Kind != keyring.RecordHTTPCredential {
			return "", ErrInvalid
		}
		rec, err := readTx(ctx, tx, id)
		if err != nil {
			return "", err
		}
		if update.ExactInvalidation {
			// A failed candidate may already have advanced the domain revision.
			// The coordinator serializes material work; never restore prior bytes.
			if rec.deleted {
				return strconv.FormatUint(rec.materialRevision, 10), nil
			}
		} else if update.ActivateOnly {
			if strconv.FormatUint(rec.materialRevision, 10) != update.ExactPublishedRevision {
				return "", ErrStale
			}
		} else if err := precondition(rec, revision); err != nil {
			return "", err
		}
		if remove {
			if err := r.readReferences(ctx, tx, &rec); err != nil {
				return "", err
			}
			if len(rec.References) != 0 {
				return "", ErrReferenced
			}
		}
		if update.ValidateOnly {
			return strconv.FormatUint(rec.materialRevision, 10), nil
		}
		var handle any
		if update.Handle != nil {
			handle = string(*update.Handle)
		}
		_, err = tx.ExecContext(ctx, `UPDATE http_credentials SET handle=?,material_revision=material_revision+1,revision=revision+1,deleted=?,updated_at=? WHERE id=?`, handle, remove, r.clock.Now().UTC().Format(time.RFC3339Nano), id)
		if err != nil {
			return "", err
		}
		action := "rotate"
		if remove {
			action = "delete"
		} else if handle == nil {
			action = "invalidate"
		}
		if err := r.auditTx(ctx, tx, id, action); err != nil {
			return "", err
		}
		return strconv.FormatUint(rec.materialRevision+1, 10), nil
	}
}
