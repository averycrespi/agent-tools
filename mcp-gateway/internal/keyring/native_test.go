//go:build keyringnative

package keyring

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nativeTestInstallationID = "01JKEYRNGNATVE000000000000"

func TestNativeDisposableKeyringRoundTrip(t *testing.T) {
	if os.Getenv("MCP_GATEWAY_KEYRING_NATIVE") != "1" {
		t.Skip("native keyring harness did not establish a disposable backend")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	provider, err := NewProvider(nativeTestInstallationID)
	require.NoError(t, err)
	capability := provider.Probe(ctx)
	require.Equal(t, contract.KeyringReady, capability.State, capability.String())
	namespace, err := NewNamespace(nativeTestInstallationID, nativeTestInstallationID, RecordStaticCredential)
	require.NoError(t, err)
	handle, err := NewHandle(rand.Reader)
	require.NoError(t, err)
	secret := []byte("disposable native keyring test value")
	t.Cleanup(func() { require.NoError(t, provider.DeleteGeneration(context.Background(), namespace, handle)) })

	require.NoError(t, provider.WriteGeneration(ctx, namespace, handle, secret))
	loaded, err := provider.ReadGeneration(ctx, namespace, handle)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(secret, loaded))
}
