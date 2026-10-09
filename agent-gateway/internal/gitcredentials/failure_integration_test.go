//go:build integration

package gitcredentials

import (
	"crypto/rand"
	"errors"
	"strconv"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestIntegrationGitCredentialLostAcknowledgmentAndStoppedRecovery(t *testing.T) {
	for boundary := 1; boundary <= 9; boundary++ {
		t.Run(strconv.Itoa(boundary), func(t *testing.T) {
			armed := false
			commits := 0
			s, _, owner := fixtureWithFault(t, func(point storage.FaultPoint) error {
				if armed && point == storage.FaultAfterCommit {
					commits++
					if commits == boundary {
						return errors.New("injected lost acknowledgment")
					}
				}
				return nil
			})
			ctx := audit.WithSystem(t.Context())
			created, err := s.Create(ctx, definition(), []byte("old-fault-private-canary"))
			require.NoError(t, err)
			armed = true
			_, err = s.Rotate(ctx, created.ID, created.Revision, []byte("candidate-fault-private-canary"))
			require.Error(t, err)
			require.Equal(t, boundary, commits)
			require.True(t, s.store.Latched())
			read, err := s.Get(ctx, created.ID)
			require.NoError(t, err)
			require.False(t, read.Available)
			_, err = s.Acquire(ctx, ref(read))
			require.Error(t, err)
			root := owner.Layout().Root
			require.NoError(t, s.store.Close())
			require.NoError(t, owner.Close())
			_, err = storage.VerifyCurrent(ctx, root)
			require.NoError(t, err)
			freshOwner, err := gatewaypaths.Acquire(root)
			require.NoError(t, err)
			defer func() { require.NoError(t, freshOwner.Close()) }()
			freshStore, err := storage.Open(ctx, freshOwner)
			require.NoError(t, err)
			defer func() { require.NoError(t, freshStore.Close()) }()
			require.NoError(t, ValidateStartup(ctx, freshStore))
			authority, err := authorization.New(freshStore, testClock{}, rand.Reader)
			require.NoError(t, err)
			provider, err := keyring.NewProvider(installation)
			require.NoError(t, err)
			require.NoError(t, provider.UseDatabaseCustody(ctx, freshOwner, freshStore))
			fresh, err := NewService(freshStore, keyring.NewCoordinator(provider, freshStore, testClock{}, rand.Reader), authority, testClock{}, rand.Reader, installation)
			require.NoError(t, err)
			retained, err := fresh.Get(ctx, created.ID)
			require.NoError(t, err)
			require.False(t, retained.Available)
			_, err = fresh.Acquire(ctx, ref(retained))
			require.Error(t, err)
		})
	}
}
