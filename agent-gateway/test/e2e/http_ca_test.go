//go:build e2e

package e2e

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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
	initialized, err := runner.Run(t.Context(), binary, "initialize", "--data-dir", root, "--secret-output", filepath.Join(t.TempDir(), "admin"), "--output", "json")
	require.NoError(t, err)
	var identity struct {
		InstallationID string `json:"installation_id"`
	}
	require.NoError(t, json.Unmarshal(initialized.Stdout, &identity))
	require.NotEmpty(t, identity.InstallationID)
	run := func(operation string, confirm bool) (testutil.ProcessResult, error) {
		args := []string{"http", "ca", operation, "--data-dir", root, "--installation-id", identity.InstallationID}
		if confirm {
			args = append(args, "--confirm")
		}
		return runner.Run(t.Context(), binary, args...)
	}
	missing, err := run("create", false)
	require.Error(t, err)
	require.Equal(t, 2, missing.ExitCode)
	created, err := run("create", true)
	require.NoError(t, err)
	require.Empty(t, created.Stderr)
	require.Contains(t, string(created.Stdout), "update client trust")
	duplicate, err := run("create", true)
	require.Error(t, err)
	require.Equal(t, 7, duplicate.ExitCode)
	exported, err := run("export", false)
	require.NoError(t, err)
	require.Empty(t, exported.Stderr)
	block, rest := pem.Decode(exported.Stdout)
	require.NotNil(t, block)
	require.Equal(t, "CERTIFICATE", block.Type)
	require.Empty(t, rest)
	ca, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.True(t, ca.IsCA)
	require.NoError(t, ca.CheckSignatureFrom(ca))
	again, err := run("export", false)
	require.NoError(t, err)
	require.Equal(t, exported.Stdout, again.Stdout)
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	for _, operation := range []string{"create", "replace", "export"} {
		busy, err := run(operation, operation != "export")
		require.Error(t, err)
		require.Equal(t, 5, busy.ExitCode)
		require.Empty(t, busy.Stdout)
	}
	require.NoError(t, owner.MarkClean())
	require.NoError(t, owner.Close())
	replaced, err := run("replace", true)
	require.NoError(t, err)
	require.NotContains(t, string(replaced.Stdout), "PRIVATE KEY")
	replacement, err := run("export", false)
	require.NoError(t, err)
	require.NotEqual(t, exported.Stdout, replacement.Stdout)
	// The e2e provider is deliberately process-local. This proves public metadata
	// continuity, not native signing persistence or client trust installation.
}
