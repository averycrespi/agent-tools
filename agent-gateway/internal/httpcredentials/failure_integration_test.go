//go:build integration

package httpcredentials

import (
	"database/sql"
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

func TestIntegrationRotationLostAcknowledgmentFailsClosedAtEveryCommit(t *testing.T) {
	for boundary := 1; boundary <= 9; boundary++ {
		t.Run(strconv.Itoa(boundary), func(t *testing.T) {
			armed := false
			commits := 0
			s, store, _ := fixtureWithFault(t, func(point storage.FaultPoint) error {
				if armed && point == storage.FaultAfterCommit {
					commits++
					if commits == boundary {
						return errors.New("injected lost acknowledgment")
					}
				}
				return nil
			})
			ctx := audit.WithSystem(t.Context())
			created, err := s.Create(ctx, testDefinition(), []byte("old-private-canary"))
			require.NoError(t, err)
			armed = true
			_, err = s.Rotate(ctx, created.ID, created.Revision, []byte("candidate-private-canary"))
			require.Error(t, err)
			require.Equal(t, boundary, commits)
			require.True(t, store.Latched())
			read, err := s.Get(ctx, created.ID)
			require.NoError(t, err)
			require.False(t, read.Available)
			_, err = s.Acquire(ctx, resourceRef(t, read))
			require.Error(t, err)
			_, err = s.Acquire(ctx, resourceRef(t, created))
			require.Error(t, err)
		})
	}
}

func TestIntegrationReferenceWriterExcludesConcurrentCredentialDelete(t *testing.T) {
	s, store, _ := fixture(t)
	ctx := audit.WithSystem(t.Context())
	created, err := s.Create(ctx, testDefinition(), []byte("private-canary"))
	require.NoError(t, err)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- store.Mutate(ctx, func(tx *sql.Tx) error {
			close(entered)
			<-release
			_, err := tx.ExecContext(ctx, `INSERT INTO test_http_grant_references VALUES(?,?,?)`, "01ARZ3NDEKTSV4RRFFQ69G5FAW", created.ID, `{"version":1,"type":"allow_requests","credential_id":"`+created.ID+`","request":{"origin":{"scheme":"https","host":"api.example.com","port":443},"methods":{"any":true},"path":{"kind":"any"}}}`)
			return err
		})
	}()
	<-entered
	err = s.Delete(ctx, created.ID, created.Revision)
	close(release)
	require.Error(t, err)
	require.NoError(t, <-done)
	require.ErrorIs(t, s.Delete(ctx, created.ID, created.Revision), ErrReferenced)
	read, err := s.Get(ctx, created.ID)
	require.NoError(t, err)
	require.True(t, read.Available)
}

func TestIntegrationStartupRejectsMalformedCredentialMetadata(t *testing.T) {
	s, store, _ := fixture(t)
	ctx := audit.WithSystem(t.Context())
	created, err := s.Create(ctx, testDefinition(), []byte("private-canary"))
	require.NoError(t, err)
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE http_credentials SET header='Host' WHERE id=?`, created.ID)
		return err
	}))
	require.ErrorIs(t, ValidateStartup(ctx, store), ErrInvalid)
	_, err = s.Acquire(ctx, contract.HTTPRevisionRef{ID: created.ID, Revision: 2})
	require.Error(t, err)
}
