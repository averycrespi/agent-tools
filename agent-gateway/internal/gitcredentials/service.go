// Package gitcredentials owns Git-only protected material over the shared
// keyring generation coordinator and singular authorization admission owner.
package gitcredentials

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

var (
	ErrInvalid     = errors.New("invalid Git credential")
	ErrNotFound    = errors.New("git credential not found")
	ErrStale       = errors.New("git credential revision is stale")
	ErrReferenced  = errors.New("git credential is referenced")
	ErrLimit       = errors.New("git credential limit reached")
	ErrUnavailable = errors.New("git credential unavailable")
)

const identityLimit = 1024

type Clock interface{ Now() time.Time }
type Service struct {
	store          *storage.Store
	coordinator    *keyring.Coordinator
	authority      *authorization.Repository
	clock          Clock
	entropy        io.Reader
	installationID string
	mutation       chan struct{}
}

type record struct {
	contract.GitCredential
	handle           sql.NullString
	materialRevision uint64
	deleted          bool
}

type generation struct {
	Version int    `json:"version"`
	Secret  string `json:"secret"`
}

func NewService(store *storage.Store, coordinator *keyring.Coordinator, authority *authorization.Repository, clock Clock, entropy io.Reader, installationID string) (*Service, error) {
	if store == nil || coordinator == nil || authority == nil || clock == nil || entropy == nil || !contract.ValidAuditID(installationID) {
		return nil, ErrInvalid
	}
	return &Service{store: store, coordinator: coordinator, authority: authority, clock: clock, entropy: entropy, installationID: installationID, mutation: make(chan struct{}, 1)}, nil
}
func Normalize(def contract.GitCredentialDefinition) (contract.GitCredentialDefinition, error) {
	if !utf8.ValidString(def.Name) || len(def.Name) == 0 || len(def.Name) > 256 || strings.TrimSpace(def.Name) != def.Name || !contract.ValidHTTPCredentialRecipe(def.Recipe) {
		return def, ErrInvalid
	}
	for _, r := range def.Name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return def, ErrInvalid
		}
	}
	origin, err := gitpolicy.Origin(def.Origin)
	if err != nil {
		return def, ErrInvalid
	}
	def.Origin = origin
	def.Recipe.Header = http.CanonicalHeaderKey(def.Recipe.Header)
	return def, nil
}
func readTx(ctx context.Context, tx *sql.Tx, id string) (record, error) {
	var r record
	err := tx.QueryRowContext(ctx, `SELECT id,name,origin,header,prefix,revision,material_revision,handle,deleted,created_at,updated_at FROM git_credentials WHERE id=?`, id).Scan(&r.ID, &r.Name, &r.Origin, &r.Recipe.Header, &r.Recipe.Prefix, &r.Revision, &r.materialRevision, &r.handle, &r.deleted, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	r.References = []contract.GitCredentialReference{}
	return r, err
}
func availabilityTx(ctx context.Context, tx *sql.Tx, r *record) error {
	var current int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM keyring_authorities a WHERE a.owner=? AND a.kind='git_credential' AND a.handle=? AND a.revision=? AND NOT EXISTS (SELECT 1 FROM keyring_authority_fences f WHERE f.owner=a.owner AND f.kind=a.kind)`, r.ID, r.handle, r.materialRevision).Scan(&current)
	r.Available = !r.deleted && r.handle.Valid && current == 1
	return err
}
func referencesTx(ctx context.Context, tx *sql.Tx, r *record) error {
	refs, err := authorization.GitCredentialReferencesTx(ctx, tx, r.ID)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		r.References = append(r.References, contract.GitCredentialReference{ID: ref.ID})
	}
	return nil
}
func (s *Service) Get(ctx context.Context, id string) (contract.GitCredential, error) {
	var rec record
	err := s.store.View(ctx, func(tx *sql.Tx) error {
		var e error
		rec, e = readTx(ctx, tx, id)
		if e != nil {
			return e
		}
		if rec.deleted {
			return ErrNotFound
		}
		if e := referencesTx(ctx, tx, &rec); e != nil {
			return e
		}
		return availabilityTx(ctx, tx, &rec)
	})
	if err != nil {
		return contract.GitCredential{}, err
	}
	if s.store.Latched() {
		rec.Available = false
	}
	return rec.GitCredential, nil
}
func (s *Service) List(ctx context.Context) ([]contract.GitCredential, error) {
	out := []contract.GitCredential{}
	err := s.store.View(ctx, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT id FROM git_credentials WHERE deleted=0 ORDER BY id LIMIT ?`, contract.GitCredentials+1)
		if e != nil {
			return e
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e := rows.Scan(&id); e != nil {
				_ = rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		if e := errors.Join(rows.Err(), rows.Close()); e != nil {
			return e
		}
		if len(ids) > contract.GitCredentials {
			return ErrInvalid
		}
		for _, id := range ids {
			rec, e := readTx(ctx, tx, id)
			if e != nil {
				return e
			}
			if e := referencesTx(ctx, tx, &rec); e != nil {
				return e
			}
			if e := availabilityTx(ctx, tx, &rec); e != nil {
				return e
			}
			if s.store.Latched() {
				rec.Available = false
			}
			out = append(out, rec.GitCredential)
		}
		return nil
	})
	return out, err
}
func (s *Service) acquire() bool {
	select {
	case s.mutation <- struct{}{}:
		return true
	default:
		return false
	}
}
func (s *Service) release() { <-s.mutation }
func (s *Service) mutate(ctx context.Context, fn func(*sql.Tx) error) error {
	release, err := s.authority.AcquireGitCredentialAuthority(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.store.Mutate(ctx, fn)
}
func (s *Service) Create(ctx context.Context, def contract.GitCredentialDefinition, secret []byte) (contract.GitCredential, error) {
	defer clear(secret)
	if !s.acquire() {
		return contract.GitCredential{}, ErrUnavailable
	}
	ctx, finishCleanup := keyring.DeferCleanupDiagnostics(ctx)
	defer func() { s.release(); finishCleanup(string(secret)) }()
	def, err := Normalize(def)
	if err != nil || !contract.ValidHTTPCredentialSecret(def.Recipe, secret) {
		return contract.GitCredential{}, ErrInvalid
	}
	id, err := s.newID(s.clock.Now())
	if err != nil {
		return contract.GitCredential{}, ErrUnavailable
	}
	err = s.mutate(ctx, func(tx *sql.Tx) error {
		var total, active int
		if e := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(deleted=0),0) FROM git_credentials`).Scan(&total, &active); e != nil {
			return e
		}
		if total >= identityLimit || active >= contract.GitCredentials {
			return ErrLimit
		}
		now := s.clock.Now().UTC().Format(contract.AuditTimestampLayout)
		if _, e := tx.ExecContext(ctx, `INSERT INTO git_credentials(id,name,origin,header,prefix,revision,material_revision,deleted,created_at,updated_at) VALUES(?,?,?,?,?,1,0,0,?,?)`, id, def.Name, def.Origin, def.Recipe.Header, def.Recipe.Prefix, now, now); e != nil {
			return e
		}
		if e := authorization.AdvanceGitCredentialRevisionTx(ctx, tx); e != nil {
			return e
		}
		return s.auditTx(ctx, tx, id, "create")
	})
	if err != nil {
		return contract.GitCredential{}, err
	}
	return s.rotate(ctx, id, "1", secret)
}
func checkPrecondition(r record, revision string) error {
	if r.deleted {
		return ErrNotFound
	}
	if !gitpolicy.ValidRevision(revision) || r.Revision != revision {
		return ErrStale
	}
	return nil
}
func (s *Service) Update(ctx context.Context, id, revision string, def contract.GitCredentialDefinition) (contract.GitCredential, error) {
	if !s.acquire() {
		return contract.GitCredential{}, ErrUnavailable
	}
	defer s.release()
	def, err := Normalize(def)
	if err != nil {
		return contract.GitCredential{}, err
	}
	err = s.mutate(ctx, func(tx *sql.Tx) error {
		rec, e := readTx(ctx, tx, id)
		if e != nil {
			return e
		}
		if e := checkPrecondition(rec, revision); e != nil {
			return e
		}
		if e := referencesTx(ctx, tx, &rec); e != nil {
			return e
		}
		if len(rec.References) > 0 && (def.Origin != rec.Origin || def.Recipe != rec.Recipe) {
			return ErrReferenced
		}
		if _, e := tx.ExecContext(ctx, `UPDATE git_credentials SET name=?,origin=?,header=?,prefix=?,revision=revision+1,updated_at=? WHERE id=?`, def.Name, def.Origin, def.Recipe.Header, def.Recipe.Prefix, s.clock.Now().UTC().Format(contract.AuditTimestampLayout), id); e != nil {
			return e
		}
		if e := authorization.AdvanceGitCredentialRevisionTx(ctx, tx); e != nil {
			return e
		}
		return s.auditTx(ctx, tx, id, "update")
	})
	if err != nil {
		return contract.GitCredential{}, err
	}
	return s.Get(ctx, id)
}
func (s *Service) Rotate(ctx context.Context, id, revision string, secret []byte) (contract.GitCredential, error) {
	defer clear(secret)
	if !s.acquire() {
		return contract.GitCredential{}, ErrUnavailable
	}
	ctx, finishCleanup := keyring.DeferCleanupDiagnostics(ctx)
	defer func() { s.release(); finishCleanup(string(secret)) }()
	return s.rotate(ctx, id, revision, secret)
}
func (s *Service) rotate(ctx context.Context, id, revision string, secret []byte) (contract.GitCredential, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return contract.GitCredential{}, err
	}
	if current.Revision != revision {
		return contract.GitCredential{}, ErrStale
	}
	if !contract.ValidHTTPCredentialSecret(current.Recipe, secret) {
		return contract.GitCredential{}, ErrInvalid
	}
	payload, err := json.Marshal(generation{Version: 1, Secret: string(secret)})
	if err != nil {
		return contract.GitCredential{}, ErrInvalid
	}
	defer clear(payload)
	ns, err := keyring.NewNamespace(s.installationID, id, keyring.RecordGitCredential)
	if err != nil {
		return contract.GitCredential{}, ErrInvalid
	}
	ctx = keyring.WithGitAuthorityAdmission(ctx, s.authority.AcquireGitCredentialAuthority)
	_, err = s.coordinator.ReplaceFencedAfterAuthorizationSuccess(ctx, ns, payload, s.callback(id, revision, false))
	if err != nil {
		return contract.GitCredential{}, err
	}
	return s.Get(ctx, id)
}
func (s *Service) Delete(ctx context.Context, id, revision string) error {
	if !s.acquire() {
		return ErrUnavailable
	}
	ctx, finishCleanup := keyring.DeferCleanupDiagnostics(ctx)
	defer func() { s.release(); finishCleanup() }()
	ns, err := keyring.NewNamespace(s.installationID, id, keyring.RecordGitCredential)
	if err != nil {
		return ErrInvalid
	}
	ctx = keyring.WithGitAuthorityAdmission(ctx, s.authority.AcquireGitCredentialAuthority)
	_, err = s.coordinator.InvalidateFenced(ctx, ns, s.callback(id, revision, true))
	return err
}
func (s *Service) auditTx(ctx context.Context, tx *sql.Tx, id, action string) error {
	return audit.MutationTx(ctx, tx, s.clock.Now(), "git_credential", action, contract.AuditTarget{Type: "git_credential", ID: id})
}
func (s *Service) callback(id, revision string, remove bool) keyring.AuthorityCallback {
	return func(ctx context.Context, tx *sql.Tx, update keyring.AuthorityUpdate) (string, error) {
		if update.Owner != id || update.Kind != keyring.RecordGitCredential {
			return "", ErrInvalid
		}
		rec, err := readTx(ctx, tx, id)
		if err != nil {
			return "", err
		}
		material := strconv.FormatUint(rec.materialRevision, 10)
		if update.ExactInvalidation {
			if update.PriorPublishedRevision != "" {
				if material != update.PriorPublishedRevision {
					return "", ErrStale
				}
			} else if rec.Revision != revision {
				return "", ErrStale
			}
			if rec.deleted {
				return material, nil
			}
		} else if update.ActivateOnly {
			if material != update.ExactPublishedRevision || rec.deleted || !rec.handle.Valid {
				return "", ErrStale
			}
		} else if err := checkPrecondition(rec, revision); err != nil {
			return "", err
		}
		if remove {
			if err := referencesTx(ctx, tx, &rec); err != nil {
				return "", err
			}
			if len(rec.References) > 0 {
				return "", ErrReferenced
			}
		}
		if err := authorization.AdvanceGitCredentialRevisionTx(ctx, tx); err != nil {
			return "", err
		}
		if update.ValidateOnly {
			return material, nil
		}
		var handle any
		if update.Handle != nil {
			handle = string(*update.Handle)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE git_credentials SET handle=?,material_revision=material_revision+1,revision=revision+1,deleted=?,updated_at=? WHERE id=?`, handle, remove, s.clock.Now().UTC().Format(contract.AuditTimestampLayout), id); err != nil {
			return "", err
		}
		action := "rotate"
		if remove {
			action = "delete"
		} else if handle == nil {
			action = "invalidate"
		}
		if err := s.auditTx(ctx, tx, id, action); err != nil {
			return "", err
		}
		return strconv.FormatUint(rec.materialRevision+1, 10), nil
	}
}

type Material struct {
	ref        contract.GitRevisionRef
	origin     string
	recipe     contract.HTTPCredentialRecipe
	secret     []byte
	generation string
}

func (m *Material) Clear() {
	if m != nil {
		clear(m.secret)
		m.secret = nil
	}
}
func (m *Material) Generation() string                 { return m.generation }
func (m *Material) Reference() contract.GitRevisionRef { return m.ref }
func (m *Material) Apply(raw string, headers http.Header) (http.Header, error) {
	locator, err := gitpolicy.Locator(raw)
	if err != nil || len(m.secret) == 0 {
		return nil, ErrUnavailable
	}
	u, _ := url.Parse(locator)
	if "https://"+u.Host != m.origin {
		return nil, ErrInvalid
	}
	for _, connection := range headers.Values("Connection") {
		for _, name := range strings.Split(connection, ",") {
			if strings.EqualFold(strings.TrimSpace(name), m.recipe.Header) {
				return nil, ErrInvalid
			}
		}
	}
	out := headers.Clone()
	if out == nil {
		out = make(http.Header)
	}
	for name := range out {
		if strings.EqualFold(name, m.recipe.Header) {
			delete(out, name)
		}
	}
	out.Set(m.recipe.Header, m.recipe.Prefix+string(m.secret))
	return out, nil
}
func (s *Service) Acquire(ctx context.Context, ref contract.GitRevisionRef) (*Material, error) {
	if !contract.ValidAuditID(ref.ID) || !gitpolicy.ValidRevision(ref.Revision) {
		return nil, ErrInvalid
	}
	var result *Material
	err := s.coordinator.WithOperation(ctx, func(op *keyring.Operation) error {
		ns, err := keyring.NewNamespace(s.installationID, ref.ID, keyring.RecordGitCredential)
		if err != nil {
			return ErrInvalid
		}
		payload, selected, err := op.ReadActive(ctx, ns)
		if err != nil {
			return err
		}
		defer clear(payload)
		var decoded generation
		if strictjson.Decode(payload, &decoded, strictjson.Options{MaxBytes: contract.HTTPCredentialValueBytes*6 + 64, MaxDepth: 2, RejectUnknownMembers: true}) != nil || decoded.Version != 1 {
			return diagnostics.WithDetail(ErrUnavailable, diagnostics.Detail{Component: "git-credential", Operation: "parse generation", Resource: ref.ID, Explanation: "rule=closed_generation_v1_json_depth_2; generation values withheld"})
		}
		return s.store.View(ctx, func(tx *sql.Tx) error {
			rec, err := readTx(ctx, tx, ref.ID)
			if err != nil {
				return err
			}
			if err := availabilityTx(ctx, tx, &rec); err != nil {
				return err
			}
			if !rec.Available || rec.Revision != ref.Revision || rec.handle.String != string(selected.Handle) || strconv.FormatUint(rec.materialRevision, 10) != selected.Revision || s.store.Latched() || !contract.ValidHTTPCredentialSecret(rec.Recipe, []byte(decoded.Secret)) {
				return diagnostics.WithDetail(ErrUnavailable, diagnostics.Detail{Component: "git-credential", Operation: "fence generation", Resource: ref.ID, Explanation: fmt.Sprintf("rule=selected_material_fence available=%t expected_revision=%s observed_revision=%s binding_matches=%t expected_generation=%d observed_generation=%s latched=%t valid_material=%t", rec.Available, ref.Revision, rec.Revision, rec.handle.String == string(selected.Handle), rec.materialRevision, selected.Revision, s.store.Latched(), contract.ValidHTTPCredentialSecret(rec.Recipe, []byte(decoded.Secret)))})
			}
			result = &Material{ref: ref, origin: rec.Origin, recipe: rec.Recipe, secret: []byte(decoded.Secret), generation: selected.Revision}
			return nil
		})
	})
	if err != nil {
		if result != nil {
			result.Clear()
		}
		return nil, err
	}
	return result, nil
}

// ConfirmMaterial holds the metadata guard through a caller's already-authorized
// detachment. The caller owns authority first; no provider I/O occurs here.
func (s *Service) ConfirmMaterial(ctx context.Context, ref contract.GitRevisionRef, generation string, confirm func() error) error {
	if confirm == nil || !s.acquire() {
		return ErrUnavailable
	}
	defer s.release()
	err := s.store.View(ctx, func(tx *sql.Tx) error {
		rec, err := readTx(ctx, tx, ref.ID)
		if err != nil {
			return err
		}
		if err := availabilityTx(ctx, tx, &rec); err != nil {
			return err
		}
		if !rec.Available || rec.Revision != ref.Revision || strconv.FormatUint(rec.materialRevision, 10) != generation {
			return ErrUnavailable
		}
		return nil
	})
	if err != nil {
		return err
	}
	if ctx.Err() != nil || s.store.Latched() {
		return ErrUnavailable
	}
	return confirm()
}
