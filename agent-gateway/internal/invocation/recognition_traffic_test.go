package invocation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

type trafficRecognition struct{}

func (*trafficRecognition) PrincipalDisplayNames(context.Context) (map[string]string, error) {
	return nil, nil
}
func (*trafficRecognition) InvocationServerName(context.Context, string) (*string, error) {
	name := "Server"
	return &name, nil
}
func (*trafficRecognition) InvocationGrantDescription(context.Context, string) (*string, error) {
	description := "Grant"
	return &description, nil
}

func TestInvocationRecognitionPreservesTrafficAndExport(t *testing.T) {
	traffic, _ := trafficFixture(t, nil, nil)
	repository, err := NewTrafficRepository(traffic, &repositoryClock{now: invocationTestTime}, entropyBytes(64), func(contract.Invalidation) {})
	require.NoError(t, err)
	prepared := trafficPrepared(1)
	observation := recordMCP(t, traffic, prepared)
	recordMCPCompletion(t, traffic, observation, trafficCompletion())
	original, err := repository.Get(t.Context(), prepared.InvocationID)
	require.NoError(t, err)
	names := &trafficRecognition{}
	reader, err := NewReadService(repository, names, names, names)
	require.NoError(t, err)
	before, err := reader.ExportHistory(t.Context(), 0, 256)
	require.NoError(t, err)
	item, err := reader.Get(t.Context(), prepared.InvocationID)
	require.NoError(t, err)
	require.NotNil(t, item.Recognition.Server)
	require.NotNil(t, item.Recognition.Grant)
	item.Recognition = nil
	require.Equal(t, original, item)
	after, err := reader.ExportHistory(t.Context(), 0, 256)
	require.NoError(t, err)
	require.Equal(t, before.Records, after.Records)
	encoded, err := json.Marshal(after)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "recognition")
}
