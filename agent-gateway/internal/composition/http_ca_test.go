package composition

import (
	"context"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestStoppedCAOwnershipAndPublicOnlyExport(t *testing.T) {
	ctx := t.Context()
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(ctx, owner, id)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	clock := testutil.NewFakeClock(compositionTime)
	backend := newMemoryBackend()
	factory := func(id string) (*keyring.Provider, error) { return keyring.NewProviderWithBackend(id, backend) }
	run := func(operation string) ([]byte, error) {
		return httpCA(ctx, root, id, operation, clock, rand.Reader, factory)
	}
	for _, operation := range []string{"create", "replace", "export"} {
		_, err = run(operation)
		require.ErrorIs(t, err, gatewaypaths.ErrInUse)
	}
	require.Empty(t, backend.values)
	require.NoError(t, owner.MarkClean())
	require.NoError(t, owner.Close())
	_, err = httpCA(ctx, root, "01ARZ3NDEKTSV4RRFFQ69G5FAW", "create", clock, rand.Reader, factory)
	require.Error(t, err)
	require.Empty(t, backend.values)
	_, err = run("replace")
	require.Error(t, err)
	_, err = run("export")
	require.Error(t, err)
	cert, err := run("create")
	require.NoError(t, err)
	block, rest := pem.Decode(cert)
	require.NotNil(t, block)
	require.Equal(t, "CERTIFICATE", block.Type)
	require.Empty(t, rest)
	_, err = run("create")
	require.Error(t, err)
	noProvider := func(string) (*keyring.Provider, error) { t.Fatal("public export accessed provider"); return nil, nil }
	exported, err := httpCA(ctx, root, id, "export", clock, rand.Reader, noProvider)
	require.NoError(t, err)
	require.Equal(t, cert, exported)
	replacement, err := run("replace")
	require.NoError(t, err)
	require.NotEqual(t, cert, replacement)
	// Lost protected material does not prevent public export or regenerate a CA.
	clear(backend.values)
	exported, err = httpCA(ctx, root, id, "export", clock, rand.Reader, noProvider)
	require.NoError(t, err)
	require.Equal(t, replacement, exported)
	failedFactory := func(string) (*keyring.Provider, error) { return nil, errors.New("private-provider-detail") }
	_, err = httpCA(ctx, root, id, "replace", clock, rand.Reader, failedFactory)
	require.Error(t, err)
	exported, err = run("export")
	require.NoError(t, err)
	require.Equal(t, replacement, exported)
	// Recovery increments the revision even if no usable signing material exists.
	owner, err = gatewaypaths.AcquireStoppedExisting(root)
	require.NoError(t, err)
	store, err = storage.Open(ctx, owner)
	require.NoError(t, err)
	require.NoError(t, httpca.InvalidateStaged(ctx, store, clock))
	require.NoError(t, store.Close())
	require.NoError(t, owner.Close())
	_, err = run("create")
	require.Error(t, err)
	recovered, err := run("replace")
	require.NoError(t, err)
	require.NotEqual(t, replacement, recovered)
}

func TestStoppedCARejectsAbsentRootWithoutCreatingIt(t *testing.T) {
	root := t.TempDir() + "/absent"
	_, err := httpCA(context.Background(), root, "01ARZ3NDEKTSV4RRFFQ69G5FAV", "create", testutil.NewFakeClock(compositionTime), rand.Reader, nil)
	require.Error(t, err)
	_, err = os.Stat(root)
	require.True(t, os.IsNotExist(err))
}
