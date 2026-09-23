//go:build integration

package httpcredentials

import (
	"context"
	"errors"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/stretchr/testify/require"
)

func TestIntegrationMaterialConfirmationFencesRotationThroughDetach(t *testing.T) {
	s, _, _ := fixture(t)
	ctx := audit.WithSystem(t.Context())
	created, err := s.Create(ctx, testDefinition(), []byte("private-value"))
	require.NoError(t, err)
	material, err := s.Acquire(ctx, resourceRef(t, created))
	require.NoError(t, err)
	defer material.Clear()
	entered, release := make(chan struct{}), make(chan struct{})
	settled := make(chan error, 1)
	go func() {
		settled <- s.ConfirmMaterial(ctx, material.Reference(), material.Generation(), func() error { close(entered); <-release; return nil })
	}()
	<-entered
	_, rotateErr := s.Rotate(ctx, created.ID, created.Revision, []byte("new-private-value"))
	close(release)
	require.ErrorIs(t, rotateErr, ErrUnavailable)
	require.NoError(t, <-settled)
	rotated, err := s.Rotate(ctx, created.ID, created.Revision, []byte("new-private-value"))
	require.NoError(t, err)
	called := false
	err = s.ConfirmMaterial(ctx, material.Reference(), material.Generation(), func() error { called = true; return nil })
	require.Error(t, err)
	require.False(t, called)
	current, err := s.Acquire(ctx, resourceRef(t, rotated))
	require.NoError(t, err)
	defer current.Clear()
	sentinel := errors.New("detachment refused")
	require.ErrorIs(t, s.ConfirmMaterial(ctx, current.Reference(), current.Generation(), func() error { return sentinel }), sentinel)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.Error(t, s.ConfirmMaterial(canceled, current.Reference(), current.Generation(), func() error { called = true; return nil }))
	require.False(t, called)
}
