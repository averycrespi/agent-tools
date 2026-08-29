package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAdministratorBearer = "mgw_admin_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestCLIAutomaticBearerSelection(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(dataDir, 0o700))
	defaultPath := filepath.Join(dataDir, gatewaypaths.AdminBearerName)
	require.NoError(t, os.WriteFile(defaultPath, []byte(testAdministratorBearer+"\n"), 0o600))

	t.Run("resolved default", func(t *testing.T) {
		command := newRootCmd()
		require.NoError(t, command.PersistentFlags().Set("data-dir", dataDir))
		status, _, err := command.Find([]string{"status"})
		require.NoError(t, err)
		selected, failure := acquireOnlineAdminBearer(status, &onlineOptions{})
		require.Nil(t, failure)
		assert.Equal(t, testAdministratorBearer, selected.value)
		assert.Equal(t, defaultPath, selected.path)
	})

	t.Run("explicit file is selected without default fallback", func(t *testing.T) {
		explicitPath := filepath.Join(t.TempDir(), "malformed")
		require.NoError(t, os.WriteFile(explicitPath, []byte("not-a-bearer\n"), 0o600))
		command := newRootCmd()
		require.NoError(t, command.PersistentFlags().Set("data-dir", dataDir))
		status, _, err := command.Find([]string{"status"})
		require.NoError(t, err)
		_, failure := acquireOnlineAdminBearer(status, &onlineOptions{bearerFile: explicitPath})
		require.NotNil(t, failure)
		assert.Equal(t, "client_bearer_malformed", failure.Code)
		assert.Contains(t, failure.Title, explicitPath)
	})

	t.Run("stdin is selected before the default", func(t *testing.T) {
		command := newRootCmd()
		require.NoError(t, command.PersistentFlags().Set("data-dir", dataDir))
		status, _, err := command.Find([]string{"status"})
		require.NoError(t, err)
		status.SetIn(strings.NewReader(testAdministratorBearer + "\n"))
		selected, failure := acquireOnlineAdminBearer(status, &onlineOptions{bearerStdin: true})
		require.Nil(t, failure)
		assert.Equal(t, testAdministratorBearer, selected.value)
		assert.Empty(t, selected.path)
	})

	t.Run("conflicts fail before selection", func(t *testing.T) {
		command := newRootCmd()
		status, _, err := command.Find([]string{"status"})
		require.NoError(t, err)
		_, failure := acquireOnlineAdminBearer(status, &onlineOptions{bearerFile: defaultPath, bearerStdin: true})
		require.NotNil(t, failure)
		assert.Equal(t, "client_bearer_source_conflict", failure.Code)
	})

	t.Run("root data directory applies before offline subcommands", func(t *testing.T) {
		command := newRootCmd()
		require.NoError(t, command.PersistentFlags().Set("data-dir", dataDir))
		serve, _, err := command.Find([]string{"serve"})
		require.NoError(t, err)
		assert.Equal(t, dataDir, selectedDataDir(serve, ""))
	})

	t.Run("API rejection names the selected file safely", func(t *testing.T) {
		failure := evaluateOnlineResponse(controlclient.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Content-Type": []string{controlclient.MediaTypeProblemJSON}},
			Body:       []byte(`{"status":401,"code":"authentication_required","title":"Authentication is required."}`),
		}, defaultPath)
		require.NotNil(t, failure)
		assert.Equal(t, "client_bearer_rejected", failure.Code)
		assert.Contains(t, failure.Title, defaultPath)
	})
}
