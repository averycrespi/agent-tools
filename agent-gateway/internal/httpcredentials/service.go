package httpcredentials

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

type Service struct {
	repository     *Repository
	coordinator    *keyring.Coordinator
	installationID string
	mutation       chan struct{}
}

type generation struct {
	Version int    `json:"version"`
	Secret  string `json:"secret"`
}

func NewService(repository *Repository, coordinator *keyring.Coordinator, installationID string) (*Service, error) {
	if repository == nil || coordinator == nil || !contract.ValidAuditID(installationID) {
		return nil, ErrInvalid
	}
	return &Service{repository: repository, coordinator: coordinator, installationID: installationID, mutation: make(chan struct{}, 1)}, nil
}

func (s *Service) List(ctx context.Context) ([]Resource, error) { return s.repository.List(ctx) }
func (s *Service) Get(ctx context.Context, id string) (Resource, error) {
	return s.repository.Get(ctx, id)
}
func (s *Service) Update(ctx context.Context, id, revision string, def Definition) (Resource, error) {
	if !s.acquireMutation() {
		return Resource{}, ErrUnavailable
	}
	defer s.releaseMutation()
	return s.repository.update(ctx, id, revision, def)
}

func (s *Service) Create(ctx context.Context, def Definition, secret []byte) (Resource, error) {
	defer clear(secret)
	if !s.acquireMutation() {
		return Resource{}, ErrUnavailable
	}
	defer s.releaseMutation()
	canonical, err := Normalize(def)
	if err != nil || !contract.ValidHTTPCredentialSecret(canonical.Recipe, secret) {
		return Resource{}, ErrInvalid
	}
	created, err := s.repository.create(ctx, canonical)
	if err != nil {
		return Resource{}, err
	}
	return s.rotate(ctx, created.ID, created.Revision, secret)
}

func (s *Service) Rotate(ctx context.Context, id, revision string, secret []byte) (Resource, error) {
	defer clear(secret)
	if !s.acquireMutation() {
		return Resource{}, ErrUnavailable
	}
	defer s.releaseMutation()
	return s.rotate(ctx, id, revision, secret)
}

func (s *Service) rotate(ctx context.Context, id, revision string, secret []byte) (Resource, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return Resource{}, err
	}
	if current.Revision != revision {
		return Resource{}, ErrStale
	}
	if !contract.ValidHTTPCredentialSecret(current.Recipe, secret) {
		return Resource{}, ErrInvalid
	}
	payload, err := json.Marshal(generation{Version: 1, Secret: string(secret)})
	if err != nil {
		return Resource{}, ErrInvalid
	}
	defer clear(payload)
	namespace, err := keyring.NewNamespace(s.installationID, id, keyring.RecordHTTPCredential)
	if err != nil {
		return Resource{}, ErrInvalid
	}
	// This cutover arms a durable fence before external work and activates only
	// an acknowledged, verified generation. Uncertainty never revives the old
	// secret, even when deletion of its non-authoritative chunks fails.
	_, err = s.coordinator.ReplaceFencedAfterAuthorizationSuccess(ctx, namespace, payload, s.repository.callback(id, revision, false))
	if err != nil {
		return Resource{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id, revision string) error {
	if !s.acquireMutation() {
		return ErrUnavailable
	}
	defer s.releaseMutation()
	namespace, err := keyring.NewNamespace(s.installationID, id, keyring.RecordHTTPCredential)
	if err != nil {
		return ErrInvalid
	}
	_, err = s.coordinator.InvalidateFenced(ctx, namespace, s.repository.callback(id, revision, true))
	return err
}

func (s *Service) acquireMutation() bool {
	select {
	case s.mutation <- struct{}{}:
		return true
	default:
		return false
	}
}
func (s *Service) releaseMutation() { <-s.mutation }

// Acquire pins metadata and material under the coordinator's material operation
// owner, then rechecks the exact metadata revision on the control read view.
// It supplies no public read API and never falls back to another generation.
func (s *Service) Acquire(ctx context.Context, ref contract.HTTPRevisionRef) (*Material, error) {
	var result *Material
	err := s.coordinator.WithOperation(ctx, func(operation *keyring.Operation) error {
		namespace, err := keyring.NewNamespace(s.installationID, ref.ID, keyring.RecordHTTPCredential)
		if err != nil {
			return ErrInvalid
		}
		payload, selected, err := operation.ReadActive(ctx, namespace)
		if err != nil {
			return err
		}
		defer clear(payload)
		var decoded generation
		// JSON may expand each permitted ASCII byte to a six-byte escape; the
		// decoded header value still receives its independent 4096-byte bound.
		if strictjson.Decode(payload, &decoded, strictjson.Options{MaxBytes: contract.HTTPCredentialValueBytes*6 + 64, MaxDepth: 2, RejectUnknownMembers: true}) != nil || decoded.Version != 1 {
			return ErrUnavailable
		}
		return s.repository.store.View(ctx, func(tx *sql.Tx) error {
			rec, err := readTx(ctx, tx, ref.ID)
			if err != nil {
				return err
			}
			if rec.deleted || rec.Revision != strconv.FormatUint(ref.Revision, 10) || !rec.handle.Valid || rec.handle.String != string(selected.Handle) || strconv.FormatUint(rec.materialRevision, 10) != selected.Revision || s.repository.store.Latched() {
				return ErrUnavailable
			}
			result, err = newMaterial(ref, rec.Definition, []byte(decoded.Secret))
			return err
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
