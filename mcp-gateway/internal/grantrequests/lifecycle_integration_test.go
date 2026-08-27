//go:build integration

package grantrequests

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

func TestS5IntegrationRequestLifecycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(context.Background(), ownership, requestTestInstallationID)
	require.NoError(t, err)
	clock := &countingRequestClock{now: requestTestTime}
	namespaces := &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
		"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
	}}
	requests, err := New(Options{
		Store: store, Clock: clock, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x44}, 80)),
		Namespaces: namespaces, Descriptors: &fakeDescriptorInspector{descriptors: map[string]catalog.DurableDescriptor{}},
		Denies: new(fakeDenyInspector), Invalidate: func(contract.Invalidation) {},
	})
	require.NoError(t, err)
	authorityEntropy := make([]byte, 512)
	for index := range authorityEntropy {
		authorityEntropy[index] = byte(index%251 + 1)
	}
	authority, err := authorization.New(store, clock, bytes.NewReader(authorityEntropy))
	require.NoError(t, err)
	principal, err := authority.CreatePrincipal(context.Background(), authorization.CreatePrincipalRequest{
		DisplayName: "Persistent owner", Visibility: contract.VisibilityRequestable,
	})
	require.NoError(t, err)
	credential, err := authority.IssueCredential(context.Background(), principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	first, err := requests.CreateOrExisting(context.Background(), CreateRequest{
		PrincipalID: principal.Principal.ID, Policy: serverApprovalPolicy(),
	})
	require.NoError(t, err)
	_, err = authority.IssueCredential(context.Background(), principal.Principal.ID, credential.Principal.Revision)
	require.NoError(t, err)
	clock.now = requestTestTime.Add(time.Second)
	cancelled, err := requests.CancelOwned(context.Background(), principal.Principal.ID, first.Request.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.RequestCancellationCancelled, cancelled.Outcome)
	duration := "60"
	second, err := requests.CreateOrExisting(context.Background(), CreateRequest{
		PrincipalID: principal.Principal.ID,
		Policy:      contract.Policy{Scope: contract.PolicyServer, Target: "sample", DurationSeconds: &duration, FutureToolsAcknowledged: true},
	})
	require.NoError(t, err)

	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())
	ownership, err = gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err = storage.Open(context.Background(), ownership)
	require.NoError(t, err)
	defer func() {
		_ = store.Close()
		_ = ownership.Close()
	}()
	requests, err = New(Options{
		Store: store, Clock: clock, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x55}, 80)),
		Namespaces: namespaces, Descriptors: &fakeDescriptorInspector{descriptors: map[string]catalog.DurableDescriptor{}},
		Denies: new(fakeDenyInspector), Invalidate: func(contract.Invalidation) {},
	})
	require.NoError(t, err)
	authority, err = authorization.New(store, clock, bytes.NewReader(bytes.Repeat([]byte{0x66}, 128)))
	require.NoError(t, err)
	persisted, found, err := requests.GetOwned(context.Background(), principal.Principal.ID, first.Request.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, contract.RequestCancelled, persisted.State)
	clock.now = requestTestTime.Add(2 * time.Second)
	rejected, err := requests.Reject(context.Background(), RejectRequest{
		ID: second.Request.ID, ExpectedRevision: "1", Reason: contract.RejectionNotApproved,
	})
	require.NoError(t, err)
	assert.Equal(t, contract.RequestRejected, rejected.State)
	targets := &fakeStoredTargetInspector{namespaces: map[string]string{requestID(400): "sample"}}
	require.NoError(t, requests.ValidateStartup(context.Background(), authority, targets))
}
