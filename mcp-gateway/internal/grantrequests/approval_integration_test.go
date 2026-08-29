//go:build integration

package grantrequests

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

func TestAtomicApprovalAndPostCommitRecoveryIntegration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	armed := false
	store, err := storage.InitializeWithFaultInjection(context.Background(), ownership, requestTestInstallationID, func(point storage.FaultPoint) error {
		if armed && point == storage.FaultAfterCommit {
			armed = false
			return assert.AnError
		}
		return nil
	})
	require.NoError(t, err)
	clock := &countingRequestClock{now: requestTestTime}
	namespaces := &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
		"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
	}}
	invalidations := 0
	requests, err := New(Options{
		Store: store, Clock: clock, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x44}, 80)),
		Namespaces: namespaces, Descriptors: &fakeDescriptorInspector{}, Denies: new(fakeDenyInspector),
		Invalidate: func(contract.Invalidation) { invalidations++ },
	})
	require.NoError(t, err)
	authorityEntropy := make([]byte, 512)
	for index := range authorityEntropy {
		authorityEntropy[index] = byte(index%251 + 1)
	}
	authority, err := authorization.New(store, clock, bytes.NewReader(authorityEntropy))
	require.NoError(t, err)
	principal, err := authority.CreatePrincipal(context.Background(), authorization.CreatePrincipalRequest{
		DisplayName: "Approval owner", Visibility: contract.VisibilityRequestable,
	})
	require.NoError(t, err)
	created, err := requests.CreateOrExisting(context.Background(), CreateRequest{
		PrincipalID: principal.Principal.ID, Policy: serverApprovalPolicy(),
	})
	require.NoError(t, err)
	beforeRevision, err := authority.AuthorizationRevision(context.Background())
	require.NoError(t, err)
	invalidations = 0
	clock.now = requestTestTime.Add(time.Minute)
	armed = true

	_, err = requests.Approve(context.Background(), authority, ApproveRequest{
		ID: created.Request.ID, ExpectedRevision: "1", ApprovedPolicy: serverApprovalPolicy(),
	})
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	assert.True(t, store.Latched())
	assert.Zero(t, invalidations)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())
	require.NoError(t, func() error {
		_, verifyErr := storage.VerifyCurrent(context.Background(), root)
		return verifyErr
	}())

	ownership, err = gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err = storage.Open(context.Background(), ownership)
	require.NoError(t, err)
	defer func() {
		_ = store.Close()
		_ = ownership.Close()
	}()
	var state, revision, grantID string
	var grantCount int64
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		if err := transaction.QueryRow(`SELECT state, revision, approved_grant_id FROM grant_requests WHERE id = ?`, created.Request.ID).Scan(&state, &revision, &grantID); err != nil {
			return err
		}
		return transaction.QueryRow(`SELECT count(*) FROM grants WHERE id = ? AND effect = 'allow'`, grantID).Scan(&grantCount)
	}))
	assert.Equal(t, string(contract.RequestApproved), state)
	assert.Equal(t, "2", revision)
	assert.Equal(t, int64(1), grantCount)
	reopenedAuthority, err := authorization.New(store, clock, bytes.NewReader(bytes.Repeat([]byte{0x55}, 128)))
	require.NoError(t, err)
	afterRevision, err := reopenedAuthority.AuthorizationRevision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, revisionPlusOne(t, beforeRevision), afterRevision)
}
