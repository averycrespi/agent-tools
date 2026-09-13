//go:build darwin || linux

package installation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestStoppedMigrationHostRefusals(t *testing.T) {
	for _, mode := range []string{"absent", "loaded-legacy", "loaded-canonical", "unknown-service", "legacy-plist", "canonical-plist", "running-legacy", "running-canonical", "running-custom", "invalid-process", "empty-process", "process-error", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			host := hostInspector{platform: "darwin", home: func() (string, error) { return home, nil }}
			if mode == "unsupported" {
				host.platform = "linux"
			}
			if mode == "legacy-plist" || mode == "canonical-plist" {
				label := "dev.agent-tools.mcp-gateway"
				if mode == "canonical-plist" {
					label = "dev.agent-tools.agent-gateway"
				}
				directory := filepath.Join(home, "Library", "LaunchAgents")
				require.NoError(t, os.MkdirAll(directory, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(directory, label+".plist"), []byte("preserve"), 0o600))
			}
			host.run = func(_ context.Context, name string, args ...string) ([]byte, int, error) {
				if name == "/bin/launchctl" {
					label := filepath.Base(args[1])
					if mode == "unknown-service" {
						return []byte("denied"), 1, nil
					}
					if (mode == "loaded-legacy" && label == "dev.agent-tools.mcp-gateway") || (mode == "loaded-canonical" && label == "dev.agent-tools.agent-gateway") {
						return []byte("loaded"), 0, nil
					}
					return []byte("Could not find service \"" + label + "\" in domain"), 113, nil
				}
				require.Equal(t, "/bin/ps", name)
				switch mode {
				case "empty-process":
					return nil, 0, nil
				case "invalid-process":
					return []byte("invalid"), 0, nil
				case "process-error":
					return nil, 1, nil
				}
				binary := "/usr/bin/unrelated"
				switch mode {
				case "running-legacy":
					binary = "/bin/mcp-gateway"
				case "running-canonical":
					binary = "/bin/agent-gateway"
				case "running-custom":
					binary = "/custom/gateway"
				}
				return []byte(fmt.Sprintf("%d %d %s\n", os.Getuid(), os.Getpid()+100, binary)), 0, nil
			}
			err := host.stopped(t.Context(), "/custom/gateway")
			if mode == "absent" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestMigrationRequiresExplicitSelectionBeforeInspection(t *testing.T) {
	host := hostInspector{run: func(context.Context, string, ...string) ([]byte, int, error) {
		t.Fatal("must validate selections first")
		return nil, 0, nil
	}}
	_, err := migrate(t.Context(), Selection{}, host)
	require.ErrorContains(t, err, "requires explicit")
}

func TestMigrationPreflightIdentityAndConfirmedCutover(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(parent, 0o700))
	source := filepath.Join(parent, "mcp-gateway")
	destination := filepath.Join(parent, "agent-gateway")
	owner, err := paths.Acquire(source)
	require.NoError(t, err)
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	store, err := storage.Initialize(t.Context(), owner, id)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, owner.Close())
	before, err := os.ReadFile(filepath.Join(source, paths.DatabaseName))
	require.NoError(t, err)
	home := t.TempDir()
	host := hostInspector{platform: "darwin", home: func() (string, error) { return home, nil }, run: func(_ context.Context, name string, args ...string) ([]byte, int, error) {
		if name == "/bin/launchctl" {
			return []byte("Could not find service \"" + filepath.Base(args[1]) + "\" in domain"), 113, nil
		}
		return []byte("0 1 /sbin/launchd\n"), 0, nil
	}}
	selection := Selection{Source: source, Destination: destination, InstallationID: id, ServiceBinary: "/bin/mcp-gateway"}
	preflight, err := migrate(t.Context(), selection, host)
	require.NoError(t, err)
	require.False(t, preflight.Migrated)
	_, err = os.Lstat(destination)
	require.ErrorIs(t, err, os.ErrNotExist)
	selection.InstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	selection.Confirm = true
	_, err = migrate(t.Context(), selection, host)
	require.ErrorContains(t, err, "does not match")
	selection.InstallationID = id
	result, err := migrate(t.Context(), selection, host)
	require.NoError(t, err)
	require.True(t, result.Migrated)
	after, err := os.ReadFile(filepath.Join(destination, paths.DatabaseName))
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.FileExists(t, filepath.Join(destination, paths.RunMarkerName))
	_, err = migrate(t.Context(), selection, host)
	require.ErrorContains(t, err, "already exchanged")
}

func TestBoundedInspectionOutput(t *testing.T) {
	var output boundedOutput
	n, err := output.Write([]byte(strings.Repeat("x", 1<<20)))
	require.NoError(t, err)
	require.Equal(t, 1<<20, n)
	_, err = output.Write([]byte("x"))
	require.Error(t, err)
	require.Equal(t, 1<<20, output.Len())
}
