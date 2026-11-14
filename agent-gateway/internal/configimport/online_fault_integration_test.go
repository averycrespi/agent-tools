//go:build integration

package configimport

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestIntegrationOnlineConfigurationLostCommitAcknowledgmentNeverReplays(t *testing.T) {
	ctx := audit.WithOffline(t.Context())
	owner, err := paths.Acquire(testutil.NewOwnerOnlyDataRoot(t))
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	armed := false
	injections := 0
	lost := errors.New("lost local commit acknowledgment")
	store, err := storage.InitializeWithFaultInjection(ctx, owner, "01ARZ3NDEKTSV4RRFFQ69G5FAV", func(point storage.FaultPoint) error {
		if armed && point == storage.FaultAfterCommit {
			injections++
			return lost
		}
		return nil
	})
	require.NoError(t, err)
	authority, err := authorization.New(store, onlineClock{}, rand.Reader)
	require.NoError(t, err)
	targets, err := servers.New(store, onlineClock{}, rand.Reader)
	require.NoError(t, err)
	activations := 0
	service := NewOnline(store, authority, targets, onlineClock{}.Now, func() (string, error) { return admin.NewID(onlineClock{}.Now(), rand.Reader) }, func(_ context.Context, _ []contract.ConfigCreated) { activations++ })
	raw := submission(t, onlineGraph()[0])
	armed = true
	result, err := service.Import(ctx, raw)
	require.ErrorIs(t, err, lost)
	require.Empty(t, result.Configuration)
	require.Zero(t, activations)
	require.Equal(t, 1, injections)
	require.True(t, store.Latched())
	_, err = service.Import(ctx, raw)
	require.Error(t, err)
	require.Equal(t, 1, injections)
	require.Equal(t, 1, count(t, store, ctx, "principals"))
	require.NoError(t, store.Close())
	reopened, err := storage.Open(ctx, owner)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	authority, err = authorization.New(reopened, onlineClock{}, rand.Reader)
	require.NoError(t, err)
	targets, err = servers.New(reopened, onlineClock{}, rand.Reader)
	require.NoError(t, err)
	require.NoError(t, authority.ValidateStartup(ctx, targets))
	reconciled := NewOnline(reopened, authority, targets, onlineClock{}.Now, func() (string, error) { t.Fatal("a conflict must not generate another identity"); return "", lost }, nil)
	_, err = reconciled.Preview(ctx, raw)
	require.ErrorIs(t, err, ErrConflict)
	_, err = reconciled.Import(ctx, raw)
	require.ErrorIs(t, err, ErrConflict)
	require.Equal(t, 1, count(t, reopened, ctx, "principals"))
}
