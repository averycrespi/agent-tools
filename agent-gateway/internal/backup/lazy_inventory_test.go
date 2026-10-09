package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

func TestConstructorInventoryDoesNotValidateOldPayloads(t *testing.T) {
	manager, store, owner := newBackupManager(t, nil)
	created, _, err := manager.Create(t.Context(), "authority", "inventory")
	require.NoError(t, err)
	payload := filepath.Join(owner.Layout().Backups, created.ID, databaseFile)
	damaged := []byte("retained damaged backup")
	require.NoError(t, os.WriteFile(payload, damaged, 0o600))
	rebuilt, err := New(Options{Store: store, Layout: owner.Layout(), Clock: manager.clock, Entropy: manager.entropy})
	require.NoError(t, err)
	require.Equal(t, contract.BackupIdle, rebuilt.Status().State)
	items, err := rebuilt.List(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 1)
	_, err = rebuilt.Get(t.Context(), created.ID)
	require.Error(t, err)
	actual, err := os.ReadFile(payload)
	require.NoError(t, err)
	require.Equal(t, damaged, actual)
	require.NoError(t, os.WriteFile(filepath.Join(owner.Layout().Backups, created.ID, metadataFile), []byte(`{"private-member-canary":"private-payload-canary"}`), 0o600))
	var output bytes.Buffer
	observer := diagnostics.New(&output, diagnostics.Warn)
	t.Cleanup(func() { observer.Finish(nil); <-observer.Done() })
	rebuilt, err = New(Options{Diagnostics: observer, Store: store, Layout: owner.Layout(), Clock: manager.clock, Entropy: manager.entropy})
	require.NoError(t, err, "backup administration must not prevent security construction")
	require.Equal(t, contract.BackupUnavailable, rebuilt.Status().State)
	require.True(t, observer.Finish(nil))
	require.Contains(t, output.String(), "rule=metadata_json")
	require.Contains(t, output.String(), "inventory unavailable; preserve artifacts")
	require.NotContains(t, output.String(), "private-member-canary")
	require.NotContains(t, output.String(), "private-payload-canary")
	_, err = rebuilt.List(t.Context())
	require.Error(t, err, "malformed inventory is not zero backups")
	_, _, err = rebuilt.AccountingStatus(t.Context())
	require.Error(t, err)
	actual, err = os.ReadFile(payload)
	require.NoError(t, err)
	require.Equal(t, damaged, actual)
}
