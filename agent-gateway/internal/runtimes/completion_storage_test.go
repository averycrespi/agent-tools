package runtimes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

var completionStorageTemplate = storagefixture.New("01ARZ3NDEKTSV4RRFFQ69G5FAV")

type contendedCompletionRepository struct {
	*servers.Repository
	store    *storage.Store
	attempts int
	refused  error
}

func (repository *contendedCompletionRepository) CompleteReconciliation(ctx context.Context, id string, state contract.ServerOperationState, reason *contract.PublicReason, event contract.AuditEvent) (servers.Operation, error) {
	repository.attempts++
	if repository.attempts == 1 {
		// Own a real foreign transaction exactly across terminal admission. The
		// nested completion must refuse before opening its transaction or callback.
		err := repository.store.Mutate(ctx, func(*sql.Tx) error {
			_, repository.refused = repository.Repository.CompleteReconciliation(ctx, id, state, reason, event)
			return nil
		})
		if err != nil {
			return servers.Operation{}, err
		}
		return servers.Operation{}, repository.refused
	}
	return repository.Repository.CompleteReconciliation(ctx, id, state, reason, event)
}

func TestCompletionRecoversRealStorageAdmissionRefusal(t *testing.T) {
	ctx := audit.WithSystem(t.Context())
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ownership.Close()) })
	store, err := completionStorageTemplate.Open(ctx, ownership)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	base, err := servers.New(store, testutil.NewFakeClock(time.Now()), rand.Reader)
	require.NoError(t, err)
	created, err := base.Create(ctx, servers.CreateRequest{Idempotency: &servers.IdempotencyRequest{AuthorityID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Method: "POST", Route: "/api/v1/servers", Key: "completion", RequestHash: sha256.Sum256([]byte("completion"))}, Definition: servers.Definition{
		Namespace: "completion", DisplayName: "Completion", Enabled: true,
		Transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", WorkingDirectory: "/tmp", Arguments: []string{}, Environment: map[string]string{}, SecretEnvironment: map[string]string{}},
	}})
	require.NoError(t, err)
	require.NotNil(t, created.Operation)
	repository := &contendedCompletionRepository{Repository: base, store: store}
	driver := &outcomeDriver{calls: make(chan Candidate, 8), outcomes: []Outcome{activeOutcome()}}
	manager, err := New(Options{Repository: repository, Driver: driver, Catalog: diagnosticCatalog{}})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		<-manager.Drain(ctx)
		require.True(t, manager.Wait(ctx))
	})
	manager.Trigger(created.Server.ID, &created.Operation.ID, true)
	waitSettlementWorkers(t, manager)
	require.ErrorIs(t, repository.refused, storage.ErrMutationBusy)
	require.Equal(t, 2, repository.attempts)
	operation, err := base.GetOperation(ctx, created.Operation.ID)
	require.NoError(t, err)
	require.Equal(t, contract.OperationSucceeded, operation.State)
	require.Equal(t, contract.RuntimeActive, manager.Status(created.Server.ID).State)
	require.Len(t, driver.calls, 1)
	require.False(t, store.Latched())
	reader, err := audit.NewRepository(store)
	require.NoError(t, err)
	page, err := reader.List(ctx, audit.Query{Limit: 100})
	require.NoError(t, err)
	attempts, outcomes := 0, 0
	for _, event := range page.Items {
		if event.Action == "reconcile" {
			if event.Phase == "attempt" {
				attempts++
			}
			if event.Phase == "outcome" {
				outcomes++
			}
		}
	}
	require.Equal(t, 1, attempts)
	require.Equal(t, 1, outcomes)
}
