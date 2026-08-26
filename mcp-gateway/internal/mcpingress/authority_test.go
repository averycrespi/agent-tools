package mcpingress

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

type ingressClock struct{ now time.Time }

func (clock *ingressClock) Now() time.Time { return clock.now }

type queuedAuthenticator struct {
	mu     sync.Mutex
	leases []*authorization.Lease
}

func (authenticator *queuedAuthenticator) Authenticate(context.Context, string) (*authorization.Lease, error) {
	authenticator.mu.Lock()
	defer authenticator.mu.Unlock()
	if len(authenticator.leases) == 0 {
		return nil, authorization.ErrAuthenticationRequired
	}
	lease := authenticator.leases[0]
	authenticator.leases = authenticator.leases[1:]
	return lease, nil
}

type testAuthority struct {
	repository *authorization.Repository
	store      *storage.Store
	mu         sync.Mutex
	bearers    map[string]string
	principals map[string]string
	leases     []*authorization.Lease
}

func newTestAuthority(t *testing.T) *testAuthority {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(context.Background(), ownership, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	entropy := make([]byte, 128*1024)
	for index := range entropy {
		entropy[index] = byte(index%251 + 1)
	}
	repository, err := authorization.New(store, &ingressClock{now: time.Date(2026, 8, 25, 18, 0, 0, 0, time.UTC)}, bytes.NewReader(entropy))
	require.NoError(t, err)
	authority := &testAuthority{
		repository: repository, store: store, bearers: make(map[string]string), principals: make(map[string]string),
	}
	t.Cleanup(func() {
		authority.releaseAll()
		if !store.Latched() {
			_ = store.Close()
		}
		_ = ownership.Close()
	})
	return authority
}

func (authority *testAuthority) add(t *testing.T, alias string, visibility contract.PrincipalVisibility) {
	t.Helper()
	creation, err := authority.repository.CreatePrincipal(context.Background(), authorization.CreatePrincipalRequest{
		DisplayName: "Agent " + alias, Visibility: visibility,
	})
	require.NoError(t, err)
	credential, err := authority.repository.IssueCredential(context.Background(), creation.Principal.ID, creation.Principal.Revision)
	require.NoError(t, err)
	authority.mu.Lock()
	authority.bearers[contract.AgentBearerPrefix+alias] = credential.Bearer
	authority.principals[alias] = creation.Principal.ID
	authority.mu.Unlock()
}

func (authority *testAuthority) Authenticate(ctx context.Context, bearer string) (*authorization.Lease, error) {
	authority.mu.Lock()
	actual := authority.bearers[bearer]
	authority.mu.Unlock()
	if actual == "" {
		return nil, authorization.ErrAuthenticationRequired
	}
	lease, err := authority.repository.Authenticate(ctx, actual)
	if lease != nil {
		authority.mu.Lock()
		authority.leases = append(authority.leases, lease)
		authority.mu.Unlock()
	}
	return lease, err
}

func (authority *testAuthority) captured() []*authorization.Lease {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return append([]*authorization.Lease(nil), authority.leases...)
}

func (authority *testAuthority) releaseAll() {
	for _, lease := range authority.captured() {
		lease.Release()
	}
}

func leaseDone(lease *authorization.Lease) bool {
	select {
	case <-lease.Done():
		return true
	default:
		return false
	}
}
