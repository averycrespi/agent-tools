//go:build integration

package backup

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestoreAcceptedSchemaLineagesRerunAuthorizationVerification(t *testing.T) {
	for schema := 3; schema <= storage.CurrentSchema; schema++ {
		t.Run(strconv.Itoa(schema), func(t *testing.T) {
			ctx := context.Background()
			root := filepath.Join(t.TempDir(), "gateway")
			require.NoError(t, os.Mkdir(root, 0o700))
			ownership, err := gatewaypaths.Acquire(root)
			require.NoError(t, err)
			live, err := storage.Initialize(ctx, ownership, backupTestInstallationID)
			require.NoError(t, err)
			clock := fixedClock{value: acceptedFixtureTime}
			_, err = admin.NewService(live, clock, bytes.NewReader(bytes.Repeat([]byte{0xA1}, 256))).Initialize(ctx, new(captureSink))
			require.NoError(t, err)
			layout := ownership.Layout()
			require.NoError(t, live.Close())
			require.NoError(t, ownership.Close())

			fixtureRoot := filepath.Join(t.TempDir(), "fixture")
			fixturePath, err := storage.WriteAcceptedSchemaFixtureForIntegration(ctx, fixtureRoot, backupTestInstallationID, schema)
			require.NoError(t, err)
			fixture, err := sql.Open("sqlite3", "file:"+fixturePath+"?_pragma=busy_timeout(2000)")
			require.NoError(t, err)
			_, err = fixture.ExecContext(ctx, `INSERT INTO admin_credentials
				(id, verifier, fingerprint, created_at, expires_at, status, revision)
				VALUES (?, ?, ?, ?, NULL, 'active', 1)`,
				"01ARZ3NDEKTSV4RRFFQ69G5FAV", bytes.Repeat([]byte{0xA0}, 32), "0123456789abcdef",
				acceptedFixtureTime.Format(time.RFC3339Nano))
			require.NoError(t, err)
			_, err = fixture.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
			require.NoError(t, err)
			require.NoError(t, fixture.Close())
			backupID := fmt.Sprintf("01ARZ3NDEKTSV4RRFFQ69G5F0%d", schema)
			artifactDir := filepath.Join(layout.Backups, backupID)
			require.NoError(t, os.MkdirAll(artifactDir, 0o700))
			artifactPath := filepath.Join(artifactDir, databaseFile)
			require.NoError(t, copyOwnerOnly(fixturePath, artifactPath))
			identity, err := storage.VerifyBackup(ctx, artifactPath)
			require.NoError(t, err)
			info, err := os.Stat(artifactPath)
			require.NoError(t, err)
			digest, err := digestFile(artifactPath)
			require.NoError(t, err)
			require.NoError(t, writeMetadata(filepath.Join(artifactDir, metadataFile), artifactMetadata{
				Backup: contract.Backup{
					ID: backupID, CreatedAt: acceptedFixtureTime.Format(time.RFC3339Nano), InstallationID: identity.InstallationID,
					SchemaVersion: strconv.Itoa(identity.SchemaVersion), SourceRevision: strconv.FormatUint(identity.Revision, 10),
					SizeBytes: info.Size(), SHA256: digest,
				},
				AuthorityHash: digestText("authority"), KeyHash: digestText("key"), InputHash: digestText("{}"),
			}))

			_, err = Restore(ctx, RestoreOptions{
				Root: root, BackupID: backupID, Sink: new(captureSink), Clock: clock,
				Entropy: bytes.NewReader(restoreTestEntropy(0xA2, 4096)),
			})
			require.NoError(t, err)
			ownership, err = gatewaypaths.Acquire(root)
			require.NoError(t, err)
			restored, err := storage.Open(ctx, ownership)
			require.NoError(t, err)
			targets, err := servers.New(restored, clock, bytes.NewReader(restoreTestEntropy(0xA3, 1024)))
			require.NoError(t, err)
			authority, err := authorization.New(restored, clock, bytes.NewReader(restoreTestEntropy(0xA4, 1024)))
			require.NoError(t, err)
			require.NoError(t, authority.ValidateStartup(ctx, targets))
			opened, err := restored.Identity(ctx)
			require.NoError(t, err)
			assert.Equal(t, storage.CurrentSchema, opened.SchemaVersion)
			require.NoError(t, restored.Close())
			require.NoError(t, ownership.Close())
		})
	}
}
