package servers

import (
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratedNamespaceIsTheOnlyServerKeyAndReplaysDurably(t *testing.T) {
	r, _, _ := newRepository(t, rand.Reader)
	request := CreateRequest{Definition: Definition{DisplayName: "Generated namespace", Transport: testStdioTransport()}, Idempotency: idempotency("generated-namespace", "same-body", "")}
	created, err := r.Create(t.Context(), request)
	require.NoError(t, err)
	require.Regexp(t, `^server-[0-9a-f]{12}$`, created.Server.Namespace)
	replayed, err := r.Create(t.Context(), request)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, created.Server.ID, replayed.Server.ID)
	require.Equal(t, created.Server.Namespace, replayed.Server.Namespace)
}
