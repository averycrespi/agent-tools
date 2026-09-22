package httpcredentials

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// MaterialGenerationTx projects only a positive generation counter, never a
// keyring handle or secret. The caller owns the coherent authority snapshot.
func MaterialGenerationTx(ctx context.Context, tx *sql.Tx, ref contract.HTTPRevisionRef) (string, error) {
	rec, err := readTx(ctx, tx, ref.ID)
	if err != nil {
		return "", err
	}
	if err = availabilityTx(ctx, tx, &rec); err != nil {
		return "", err
	}
	if !rec.Available || rec.Revision != strconv.FormatUint(ref.Revision, 10) {
		return "", ErrUnavailable
	}
	return strconv.FormatUint(rec.materialRevision, 10), nil
}

// ConfirmMaterial holds the same nonqueueing guard as edits, fence activation,
// rotation and deletion through dispatch detachment. It does no keyring I/O.
// Call only after acquiring authority, never while holding traffic persistence.
func (s *Service) ConfirmMaterial(ctx context.Context, ref contract.HTTPRevisionRef, generation string, confirm func() error) error {
	if s == nil || confirm == nil || !s.acquireMutation() {
		return ErrUnavailable
	}
	defer s.releaseMutation()
	err := s.repository.store.View(ctx, func(tx *sql.Tx) error {
		current, err := MaterialGenerationTx(ctx, tx, ref)
		if err != nil {
			return err
		}
		if current != generation {
			return ErrUnavailable
		}
		return nil
	})
	if err != nil {
		return err
	}
	if ctx.Err() != nil || s.repository.store.Latched() {
		return ErrUnavailable
	}
	return confirm()
}

func (m *Material) Generation() string { return m.generation }
