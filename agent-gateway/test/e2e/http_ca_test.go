//go:build e2e

package e2e

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestStoppedHTTPCAPublicCommandsRealBinary(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	runner, err := testutil.NewBinaryRunner(15*time.Second, 8192)
	require.NoError(t, err)
	binary := gatewayBinary(t)
	initialized, err := runner.Run(t.Context(), binary, "init", "--confirm", "--data-dir", root, "--json")
	require.NoError(t, err, "%s", initialized.Stderr)
	destination := filepath.Join(root, gatewaypaths.PublicCertificateName)
	initial, err := os.ReadFile(destination)
	require.NoError(t, err)
	run := func(operation string, flags ...string) (testutil.ProcessResult, error) {
		args := []string{"http", "ca", operation, "--data-dir", root}
		return runner.Run(t.Context(), binary, append(args, flags...)...)
	}
	removed, err := run("create")
	require.Error(t, err)
	require.Equal(t, 2, removed.ExitCode)
	refused, err := run("replace")
	require.Error(t, err)
	require.Equal(t, 2, refused.ExitCode)
	exported, err := run("export", "--stdout")
	require.NoError(t, err)
	require.Empty(t, exported.Stderr)
	require.Equal(t, initial, exported.Stdout)
	block, rest := pem.Decode(exported.Stdout)
	require.NotNil(t, block)
	require.Equal(t, "CERTIFICATE", block.Type)
	require.Empty(t, rest)
	ca, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.True(t, ca.IsCA)
	require.NoError(t, ca.CheckSignatureFrom(ca))
	again, err := run("export")
	require.NoError(t, err)
	require.Contains(t, string(again.Stdout), destination)
	require.Contains(t, string(again.Stdout), "sha256:")
	wrong, err := run("replace", "--confirm", "--installation-id", "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.Error(t, err)
	require.Equal(t, 5, wrong.ExitCode)
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	for _, operation := range []string{"replace", "export"} {
		flags := []string{"--stdout"}
		if operation == "replace" {
			flags = []string{"--confirm"}
		}
		busy, err := run(operation, flags...)
		require.Error(t, err)
		require.Equal(t, 5, busy.ExitCode)
		require.Empty(t, busy.Stdout)
	}
	require.NoError(t, owner.MarkClean())
	require.NoError(t, owner.Close())
	replaced, err := run("replace", "--confirm")
	require.NoError(t, err, "%s", replaced.Stderr)
	require.Contains(t, string(replaced.Stderr), "update their trust")
	require.NotContains(t, string(replaced.Stdout), "PRIVATE KEY")
	replacement, err := run("export", "--stdout")
	require.NoError(t, err)
	require.NotEqual(t, initial, replacement.Stdout)
	managed, err := os.ReadFile(destination)
	require.NoError(t, err)
	require.Equal(t, replacement.Stdout, managed)
	require.NoError(t, os.WriteFile(destination, []byte("unrelated-output"), 0600))
	blocked, err := run("replace", "--confirm")
	require.Error(t, err)
	require.Contains(t, string(blocked.Stderr), "unchanged")
	retained, err := run("export", "--stdout")
	require.NoError(t, err)
	require.Equal(t, replacement.Stdout, retained.Stdout)
	managed, err = os.ReadFile(destination)
	require.NoError(t, err)
	require.Equal(t, "unrelated-output", string(managed))
	// The process-local E2E provider proves metadata continuity, not native key
	// persistence, signing qualification, or client trust installation.
}
