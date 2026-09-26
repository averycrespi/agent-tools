package httpca

import (
	"context"
	"database/sql"
	"io"
	"strconv"
	"sync"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// Service is constructed by composition with the installation's existing
// coordinator. It never opens a keyring, storage root, listener or trust store.
type Service struct {
	mu           sync.Mutex
	store        *storage.Store
	coordinator  *keyring.Coordinator
	installation string
	clock        Clock
	entropy      io.Reader
	signer       *Signer
}

type record struct {
	revision    uint64
	handle      sql.NullString
	certificate []byte
}

func New(store *storage.Store, coordinator *keyring.Coordinator, installation string, clock Clock, entropy io.Reader) (*Service, error) {
	if store == nil || coordinator == nil || clock == nil || entropy == nil || !contract.ValidAuditID(installation) {
		return nil, ErrUnavailable
	}
	return &Service{store: store, coordinator: coordinator, installation: installation, clock: clock, entropy: entropy}, nil
}

func read(ctx context.Context, tx *sql.Tx) (record, error) {
	var r record
	err := tx.QueryRowContext(ctx, `SELECT revision,handle,certificate FROM http_ca WHERE singleton=1`).Scan(&r.revision, &r.handle, &r.certificate)
	if err != nil {
		return r, err
	}
	if r.certificate != nil {
		if _, err := publicCertificate(r.certificate); err != nil {
			return r, err
		}
	}
	if r.handle.Valid {
		if _, err := keyring.ParseHandle(r.handle.String); err != nil || r.revision == 0 || r.certificate == nil {
			return r, ErrUnavailable
		}
	}
	return r, nil
}

func ValidateStartup(ctx context.Context, store *storage.Store) error {
	return store.View(ctx, func(tx *sql.Tx) error {
		r, err := read(ctx, tx)
		if err != nil {
			return err
		}
		var invalid int
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM keyring_authorities WHERE kind='http_ca' AND (owner<>(SELECT installation_id FROM gateway_meta WHERE singleton=1) OR handle<>? OR revision<>?)`, r.handle.String, r.revision).Scan(&invalid)
		if err != nil {
			return err
		}
		if invalid != 0 {
			return ErrUnavailable
		}
		return nil
	})
}

// PublicCertificate exports only public metadata; success does not attest that
// protected signing material is currently available or install client trust.
func (s *Service) PublicCertificate(ctx context.Context) ([]byte, string, error) {
	return PublicCertificate(ctx, s.store)
}

// PublicCertificate reads only public metadata, without constructing a provider.
func PublicCertificate(ctx context.Context, store *storage.Store) ([]byte, string, error) {
	var r record
	err := store.View(ctx, func(tx *sql.Tx) error { var err error; r, err = read(ctx, tx); return err })
	if err != nil {
		return nil, "", err
	}
	cert, err := publicPEM(r.certificate)
	return cert, strconv.FormatUint(r.revision, 10), err
}

// Inspection distinguishes a never-created CA from retained or invalidated
// authority without touching protected material. Presence does not prove signing.
type Inspection struct {
	Revision    string `json:"revision"`
	Present     bool   `json:"present"`
	Selected    bool   `json:"selected"`
	Unsettled   bool   `json:"unsettled"`
	Certificate []byte `json:"-"`
}

func Inspect(ctx context.Context, store *storage.Store) (Inspection, error) {
	var result Inspection
	err := store.View(ctx, func(tx *sql.Tx) error { var err error; result, err = InspectTx(ctx, tx); return err })
	return result, err
}

func InspectTx(ctx context.Context, tx *sql.Tx) (Inspection, error) {
	r, err := read(ctx, tx)
	if err != nil {
		return Inspection{}, err
	}
	var unsettled int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM keyring_authority_fences WHERE kind='http_ca') + (SELECT count(*) FROM keyring_candidates WHERE kind='http_ca')`).Scan(&unsettled); err != nil {
		return Inspection{}, err
	}
	result := Inspection{Revision: strconv.FormatUint(r.revision, 10), Present: r.certificate != nil, Selected: r.handle.Valid, Unsettled: unsettled != 0}
	if result.Present {
		result.Certificate, err = publicPEM(r.certificate)
	}
	return result, err
}

// Revision is the safe metadata precondition for an explicitly stopped mutation.
func (s *Service) Revision(ctx context.Context) (string, error) {
	var r record
	err := s.store.View(ctx, func(tx *sql.Tx) error { var err error; r, err = read(ctx, tx); return err })
	return strconv.FormatUint(r.revision, 10), err
}

// CutoverError marks entry into authority cutover. Earlier validation and entropy
// failures leave authority unchanged; a failed cutover must not be replayed.
type CutoverError struct{ Cause error }

func (e *CutoverError) Error() string { return "CA cutover outcome uncertain" }
func (e *CutoverError) Unwrap() error { return e.Cause }

// Replace is explicit creation (expected "0") or replacement. It is never
// called by startup, Load or recovery. The caller owns stopped installation
// lifecycle; replacement requires clients to update their trust.
func (s *Service) Replace(ctx context.Context, expected string) error {
	if !s.mu.TryLock() {
		return ErrUnavailable
	}
	defer s.mu.Unlock()
	var r record
	if err := s.store.View(ctx, func(tx *sql.Tx) error { var err error; r, err = read(ctx, tx); return err }); err != nil {
		return err
	}
	if strconv.FormatUint(r.revision, 10) != expected {
		return ErrUnavailable
	}
	payload, cert, err := generate(s.installation, s.clock, s.entropy)
	if err != nil {
		return err
	}
	defer clear(payload)
	if s.signer != nil {
		s.signer.Close()
		s.signer = nil
	}
	ns, err := keyring.NewNamespace(s.installation, s.installation, keyring.RecordHTTPCA)
	if err != nil {
		return err
	}
	_, err = s.coordinator.ReplaceFencedAfterAuthorizationSuccess(ctx, ns, payload, s.callback(expected, cert))
	if err != nil {
		return &CutoverError{Cause: err}
	}
	return nil
}

func (s *Service) callback(expected string, cert []byte) keyring.AuthorityCallback {
	return func(ctx context.Context, tx *sql.Tx, u keyring.AuthorityUpdate) (string, error) {
		if u.Owner != s.installation || u.Kind != keyring.RecordHTTPCA {
			return "", ErrUnavailable
		}
		r, err := read(ctx, tx)
		if err != nil {
			return "", err
		}
		revision := strconv.FormatUint(r.revision, 10)
		if u.ActivateOnly {
			if revision != u.ExactPublishedRevision {
				return "", ErrUnavailable
			}
		} else if !u.ExactInvalidation && revision != expected {
			return "", ErrUnavailable
		}
		if u.ValidateOnly {
			return revision, nil
		}
		var handle any
		public := r.certificate
		if u.Handle != nil {
			handle = string(*u.Handle)
			public = cert
		}
		_, err = tx.ExecContext(ctx, `UPDATE http_ca SET revision=revision+1,handle=?,certificate=? WHERE singleton=1`, handle, public)
		return strconv.FormatUint(r.revision+1, 10), err
	}
}

// Load reads only selected material. Absence, corruption, key loss, fencing and
// expiry are failures, never an invitation to generate or try an old handle.
func (s *Service) Load(ctx context.Context) (*Signer, error) {
	if !s.mu.TryLock() {
		return nil, ErrUnavailable
	}
	defer s.mu.Unlock()
	var result *Signer
	err := s.coordinator.WithOperation(ctx, func(op *keyring.Operation) error {
		ns, err := keyring.NewNamespace(s.installation, s.installation, keyring.RecordHTTPCA)
		if err != nil {
			return err
		}
		payload, selected, err := op.ReadActive(ctx, ns)
		if err != nil {
			return err
		}
		defer clear(payload)
		return s.store.View(ctx, func(tx *sql.Tx) error {
			r, err := read(ctx, tx)
			if err != nil {
				return err
			}
			if !r.handle.Valid || r.handle.String != string(selected.Handle) || strconv.FormatUint(r.revision, 10) != selected.Revision || s.store.Latched() {
				return ErrUnavailable
			}
			result, err = decode(payload, r.certificate, s.installation, s.clock, s.entropy)
			return err
		})
	})
	if err != nil {
		return nil, ErrUnavailable
	}
	if s.signer != nil {
		s.signer.Close()
	}
	s.signer = result
	return result, nil
}

func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.signer != nil {
		s.signer.Close()
		s.signer = nil
	}
}

// InvalidateStaged changes only the stopped replacement database. Every restore
// requires explicit replacement, including when retired physical keys survive.
// It never touches the current installation's keyring or resurrects its handles.
func InvalidateStaged(ctx context.Context, store *storage.Store, clock Clock) error {
	identity, err := store.Identity(ctx)
	if err != nil {
		return err
	}
	return store.Mutate(ctx, func(tx *sql.Tx) error {
		if _, err := read(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM keyring_authorities WHERE kind='http_ca'`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO keyring_authority_fences(owner,kind) VALUES(?,'http_ca')`, identity.InstallationID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE http_ca SET revision=revision+1,handle=NULL WHERE singleton=1`); err != nil {
			return err
		}
		return audit.MutationTx(ctx, tx, clock.Now(), "keyring", "fence", contract.AuditTarget{Type: "installation", ID: identity.InstallationID})
	})
}
