package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/backup"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestStoppedBackupVerificationRetainsPredicateVersusNativeCause(t *testing.T) {
	for _, mode := range []string{"size", "checksum", "verification", "missing"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "gateway")
			secretPath := filepath.Join(t.TempDir(), "initial")
			initializeLegacyBackupFixture(t, root, secretPath)
			secret, err := os.ReadFile(secretPath)
			require.NoError(t, err)
			owner, err := gatewaypaths.Acquire(root)
			require.NoError(t, err)
			store, err := storage.Open(t.Context(), owner)
			require.NoError(t, err)
			manager, err := backup.New(backup.Options{Store: store, Layout: owner.Layout(), Clock: systemClock{}, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x55}, 128))})
			require.NoError(t, err)
			artifact, _, err := manager.Create(t.Context(), "authority", "diagnostic-validation")
			require.NoError(t, err)
			directory := filepath.Join(owner.Layout().Backups, artifact.ID)
			require.NoError(t, store.Close())
			require.NoError(t, owner.Close())
			database := filepath.Join(directory, "gateway.db")
			contents, err := os.ReadFile(database)
			require.NoError(t, err)
			expected := ""
			switch mode {
			case "size":
				contents = append(contents, 0)
				expected = "rule=database_size"
			case "checksum":
				contents[len(contents)-1] ^= 1
				expected = "rule=database_checksum"
			case "verification":
				contents[0] = 'X'
				metadataPath := filepath.Join(directory, "metadata.json")
				raw, err := os.ReadFile(metadataPath)
				require.NoError(t, err)
				var metadata map[string]any
				require.NoError(t, json.Unmarshal(raw, &metadata))
				digest := sha256.Sum256(contents)
				metadata["sha256"] = hex.EncodeToString(digest[:])
				raw, err = json.Marshal(metadata)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(metadataPath, raw, 0o600))
				expected = "not a database"
			case "missing":
				require.NoError(t, os.Remove(database))
				expected = "no such file"
			}
			if mode != "missing" {
				require.NoError(t, os.WriteFile(database, contents, 0o600))
			}
			var stdout, stderr bytes.Buffer
			command := newTestRootCmd(t)
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			replacement := filepath.Join(t.TempDir(), "replacement")
			command.SetArgs([]string{"maintenance", "restore-backup", artifact.ID, "--data-dir", root, "--secret-output", replacement, "--dry-run"})
			require.Error(t, command.ExecuteContext(t.Context()))
			require.Contains(t, stderr.String(), expected)
			require.NotContains(t, stderr.String(), string(bytes.TrimSpace(secret)))
			require.Empty(t, stdout.String())
			_, err = os.Stat(replacement)
			require.True(t, os.IsNotExist(err))
			if mode != "missing" {
				after, err := os.ReadFile(database)
				require.NoError(t, err)
				require.Equal(t, contents, after)
			}
		})
	}
}
