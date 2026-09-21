package invocation

import (
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPTrafficPairedRestorePreservesBothDomains(t *testing.T) {
	traffic, owner := trafficFixture(t, nil, nil)
	control, err := storage.Initialize(t.Context(), owner, invocationTestInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, control.Close()) }()
	require.NoError(t, control.SelectTraffic(t.Context(), "", invocationID(90)))
	mcp, err := traffic.Admit(t.Context(), trafficPrepared(1))
	require.NoError(t, err)
	traffic.Release(mcp)
	http, err := traffic.AdmitHTTP(t.Context(), httpTrafficAdmission(2))
	require.NoError(t, err)
	require.True(t, traffic.Confirm(t.Context(), http))
	require.NoError(t, traffic.CompleteHTTP(t.Context(), http, httpTrafficCompletion()))
	unknown, err := traffic.AdmitHTTP(t.Context(), httpTrafficAdmission(3))
	require.NoError(t, err)
	require.True(t, traffic.Confirm(t.Context(), unknown))
	root := t.TempDir()
	source := filepath.Join(root, "traffic.db")
	require.NoError(t, traffic.BackupPair(t.Context(), control, filepath.Join(root, "control.db"), source))
	require.NoError(t, traffic.Close())
	require.NoError(t, RestoreTraffic(t.Context(), owner, source, invocationTestInstallationID, invocationID(90), invocationID(91), traffic.config))
	restored, err := OpenTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(91), traffic.config)
	require.NoError(t, err)
	defer func() { require.NoError(t, restored.Close()) }()
	mh, err := restored.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, mh.Records, 1)
	assert.Equal(t, invocationID(1), mh.Records[0].InvocationID)
	hh, err := restored.HTTPHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, hh.Records, 2)
	assert.Equal(t, invocationID(91), hh.Generation)
	assert.Equal(t, int64(3), hh.HighWater)
	require.NotNil(t, hh.Records[0].Completion)
	assert.Equal(t, "succeeded", hh.Records[0].Completion.Outcome)
	assert.Nil(t, hh.Records[1].Completion)
	assert.False(t, restored.Confirm(t.Context(), unknown))
	assert.Empty(t, restored.pins)
}
