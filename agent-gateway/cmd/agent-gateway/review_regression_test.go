package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestInitCompleteIgnoresOptionalTrafficWithoutWriting(t *testing.T) {
	for _, state := range []string{"missing", "corrupt", "wal", "journal"} {
		t.Run(state, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "gateway")
			command := newTestRootCmd(t)
			command.SetOut(new(bytes.Buffer))
			command.SetErr(new(bytes.Buffer))
			command.SetArgs([]string{"init", "--data-dir", root, "--confirm"})
			require.NoError(t, command.ExecuteContext(t.Context()))
			layout, err := gatewaypaths.Resolve(root)
			require.NoError(t, err)
			identity, err := storage.InspectClosedDatabase(t.Context(), layout.Database)
			require.NoError(t, err)
			traffic := filepath.Join(root, "traffic-"+identity.TrafficGeneration+".db")
			switch state {
			case "missing":
				require.NoError(t, os.Remove(traffic))
			case "corrupt":
				require.NoError(t, os.WriteFile(traffic, []byte("corrupt"), 0o600))
			default:
				require.NoError(t, os.WriteFile(traffic+"-"+state, []byte("uncheckpointed"), 0o600))
			}
			// Missing public output or bad optional history never authorizes repair.
			require.NoError(t, os.Remove(filepath.Join(root, gatewaypaths.PublicCertificateName)))
			before := treeBytes(t, root)
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			command = newTestRootCmd(t)
			command.SetOut(stdout)
			command.SetErr(stderr)
			command.SetArgs([]string{"init", "--data-dir", root, "--confirm", "--json"})
			require.NoError(t, command.ExecuteContext(t.Context()))
			require.Contains(t, stdout.String(), `"state":"already_initialized"`)
			require.Empty(t, stderr.String())
			require.Equal(t, before, treeBytes(t, root))
		})
	}
}

func TestLifecycleManifestResolvesCanonicalCLIGrammar(t *testing.T) {
	root := newRootCmd()
	for _, row := range contract.ControlPlaneLifecycleManifest() {
		for _, use := range row.CLIUses {
			if strings.HasPrefix(use, "--") {
				command, _, err := root.Find([]string{"agent", "list"})
				require.NoError(t, err)
				require.NotNil(t, command.Flags().Lookup(strings.Fields(use)[0][2:]))
				continue
			}
			command, _, err := root.Find(strings.Fields(use))
			require.NoError(t, err, use)
			require.NotSame(t, root, command, use)
			require.Equal(t, "agent-gateway "+use, command.Parent().CommandPath()+" "+command.Use)
		}
	}
}

func TestAgentCredentialPreflightErrorsUsePublicLanguageInJSON(t *testing.T) {
	for _, test := range []struct{ etag, title string }{
		{"", "Agent credential issue does not match the current credential slot state."},
		{contract.PrincipalETag(idForSecurityTest(), "6"), "The explicit agent ETag does not match the loaded agent."},
	} {
		server, requests := newPrincipalRequestETagServer(t, idForSecurityTest())
		args := []string{"agent", "credential", "issue", idForSecurityTest(), "--yes", "--secret-output", filepath.Join(t.TempDir(), "credential")}
		if test.etag != "" {
			args = append(args, "--etag", test.etag)
		}
		output, err := executePrincipalRequestETagCommand(t, server.URL, args...)
		require.Error(t, err)
		var problem controlclient.OnlineError
		require.NoError(t, json.Unmarshal(output, &problem))
		require.Equal(t, test.title, problem.Title)
		require.Len(t, requests, 1)
		require.Equal(t, "GET", (<-requests).method)
	}
}

func TestAgentClientInputErrorsUsePublicLanguageInJSON(t *testing.T) {
	for _, args := range [][]string{{"agent", "get"}, {"agent", "update"}, {"agent", "credential", "issue"}, {"agent", "credential", "rotate"}, {"agent", "credential", "revoke"}} {
		command := newRootCmd()
		stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
		command.SetOut(stdout)
		command.SetErr(stderr)
		command.SetArgs(append(args, "invalid", "--json"))
		require.Error(t, command.ExecuteContext(t.Context()))
		var problem controlclient.OnlineError
		require.NoError(t, json.Unmarshal(stderr.Bytes(), &problem))
		require.NotContains(t, strings.ToLower(problem.Title), "principal")
		require.Contains(t, problem.Title, "ID is invalid.")
		require.Empty(t, stdout.String())
	}
}
