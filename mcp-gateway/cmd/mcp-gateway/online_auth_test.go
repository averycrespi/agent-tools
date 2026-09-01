package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestCLIStartupGuidanceAndFailureProjection(t *testing.T) {
	tests := []struct {
		name     string
		address  string
		dataDir  string
		expected string
	}{
		{name: "defaults", address: controlclient.DefaultAddress, expected: "MCP Gateway is not running. Start it with: mcp-gateway serve.\n"},
		{name: "alternate port", address: "http://127.0.0.1:9000", expected: "MCP Gateway is not running. Start it with: mcp-gateway serve --listen 127.0.0.1:9000.\n"},
		{name: "alternate loopback", address: "http://127.2.3.4:8210", expected: "MCP Gateway is not running. Start it with: mcp-gateway serve --listen 127.2.3.4:8210.\n"},
		{name: "data directory", address: controlclient.DefaultAddress, dataDir: "/tmp/custom gateway", expected: "MCP Gateway is not running. Start it with: data_dir=$(printf '%b_' '/tmp/custom gateway'); data_dir=${data_dir%_}; mcp-gateway serve --data-dir \"$data_dir\".\n"},
		{name: "combined", address: "http://127.2.3.4:9000", dataDir: "/tmp/custom gateway", expected: "MCP Gateway is not running. Start it with: data_dir=$(printf '%b_' '/tmp/custom gateway'); data_dir=${data_dir%_}; mcp-gateway serve --listen 127.2.3.4:9000 --data-dir \"$data_dir\".\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newRootCmd()
			command, _, err := root.Find([]string{"status"})
			require.NoError(t, err)
			require.NoError(t, command.Flags().Set("address", test.address))
			if test.dataDir != "" {
				require.NoError(t, root.PersistentFlags().Set("data-dir", test.dataDir))
			}
			var stderr bytes.Buffer
			command.SetErr(&stderr)
			err = writeOnlineFailure(command, string(controlclient.OutputHuman), &controlclient.OnlineError{Code: "gateway_not_running", Title: "MCP Gateway is not running.", Exit: 9})
			require.Error(t, err)
			assert.Equal(t, test.expected, stderr.String())
		})
	}

	root := newRootCmd()
	command, _, err := root.Find([]string{"status"})
	require.NoError(t, err)
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	err = writeOnlineFailure(command, string(controlclient.OutputJSON), &controlclient.OnlineError{Code: "gateway_not_running", Title: "MCP Gateway is not running.", Exit: 9})
	require.Error(t, err)
	assert.Equal(t, "{\"status\":null,\"code\":\"gateway_not_running\",\"title\":\"MCP Gateway is not running. Start it with: mcp-gateway serve.\",\"exit_code\":9,\"uncertain\":false}\n", stderr.String())

	unsafeDataDir := "/tmp/quote'line\nbreak"
	root = newRootCmd()
	command, _, err = root.Find([]string{"status"})
	require.NoError(t, err)
	require.NoError(t, root.PersistentFlags().Set("data-dir", unsafeDataDir))
	stderr.Reset()
	command.SetErr(&stderr)
	err = writeOnlineFailure(command, string(controlclient.OutputHuman), &controlclient.OnlineError{Code: "gateway_not_running", Title: "MCP Gateway is not running.", Exit: 9})
	require.Error(t, err)
	assert.NotContains(t, stderr.String(), "line\nbreak")
	assert.Contains(t, stderr.String(), `quote'"'"'line\012break`)
}

func TestGrantNameRejectsUnicodeControls(t *testing.T) {
	assert.True(t, validGrantName("Safe access"))
	assert.False(t, validGrantName("Unsafe\x1faccess"))
	assert.False(t, validGrantName("Unsafe\u0085access"))
}

func TestCLIOutputMatrix(t *testing.T) {
	root := newRootCmd()
	for _, spec := range onlineCommandSpecs() {
		command, _, err := root.Find(spec.Path)
		require.NoError(t, err)
		output := command.Flags().Lookup("output")
		require.NotNil(t, output, strings.Join(spec.Path, " "))
		assert.Equal(t, "human", output.DefValue, strings.Join(spec.Path, " "))
		assert.NotNil(t, command.Flags().Lookup("json"), strings.Join(spec.Path, " "))
	}

	categories := map[string][]string{
		"read": {
			"status", "admin credential list", "admin credential get ID", "backup list", "backup get BACKUP_ID",
			"server list", "server get ID", "server operation list ID", "server operation get ID OPERATION_ID",
			"server auth-flow list ID", "server auth-flow get ID FLOW_ID", "server descriptor list ID", "server descriptor get ID TOOL_ID",
			"catalog list", "principal list", "principal get ID", "grant list", "grant get ID",
			"grant-request list", "grant-request get REQUEST_ID", "invocation list", "invocation get INVOCATION_ID",
		},
		"mutation": {
			"backup create", "server create --file PATH", "server update ID [--etag ETAG] [--display-name NAME] [--enable|--disable] [--file PATH]", "server delete ID [--etag ETAG]",
			"server operation start ID --kind KIND [--etag ETAG]", "server credential replace ID --file PATH [--etag ETAG]",
			"principal create --display-name NAME --visibility VISIBILITY", "principal update ID [--etag ETAG] [--display-name NAME] [--visibility VISIBILITY] [--state STATE]", "principal credential revoke ID [--etag ETAG]",
			"grant create --name NAME --principal-id ID --effect EFFECT --server-id ID [--upstream-name NAME] [--expires-at RFC3339] [--file PATH]", "grant-request approve REQUEST_ID --name NAME --scope SCOPE --target TARGET [--etag ETAG] [--duration-seconds SECONDS] [--acknowledge-future-tools] [--file PATH]", "grant-request reject REQUEST_ID --reason REASON [--etag ETAG]",
		},
		"no_content": {
			"admin credential revoke ID", "backup delete BACKUP_ID", "server auth-flow cancel ID FLOW_ID", "grant delete ID",
		},
		"one_time_secret": {
			"admin credential create [--expires-at RFC3339] [--secret-output NEW_PATH]", "admin credential rotate OLD_CREDENTIAL_ID --secret-output NEW_PATH",
			"principal credential issue ID [--etag ETAG] [--secret-output NEW_PATH]", "principal credential rotate ID [--etag ETAG] [--secret-output NEW_PATH]",
		},
		"terminal_auth_flow": {"server auth-flow start ID [--etag ETAG] [--open]"},
	}
	seen := make(map[string]string)
	for category, uses := range categories {
		for _, use := range uses {
			_, duplicate := seen[use]
			assert.False(t, duplicate, use)
			seen[use] = category
		}
	}
	assert.Len(t, seen, len(onlineCommandSpecs()))
	for _, spec := range onlineCommandSpecs() {
		assert.NotEmpty(t, seen[spec.ManifestUse], spec.ManifestUse)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	command := newRootCmd()
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs([]string{"status", "--json", "--output", "human"})
	err := command.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Equal(t, 2, commandExitCode(err))
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "Choose either --output human or --output json")

	stdout.Reset()
	stderr.Reset()
	command = newRootCmd()
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs([]string{"status", "EXTRA", "--json"})
	err = command.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Equal(t, 2, commandExitCode(err))
	assert.Empty(t, stdout.String())
	assert.True(t, json.Valid(stderr.Bytes()))
}

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
