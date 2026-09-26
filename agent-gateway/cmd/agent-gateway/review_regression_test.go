package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/backup"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestInitRefusesUnverifiedSelectedTrafficWithoutWriting(t *testing.T) {
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
			identity, err := storage.VerifyBackup(t.Context(), layout.Database)
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
			// Missing public output must not authorize writes around bad traffic.
			require.NoError(t, os.Remove(filepath.Join(root, gatewaypaths.PublicCertificateName)))
			before := treeBytes(t, root)
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			command = newTestRootCmd(t)
			command.SetOut(stdout)
			command.SetErr(stderr)
			command.SetArgs([]string{"init", "--data-dir", root, "--confirm", "--json"})
			require.Error(t, command.ExecuteContext(t.Context()))
			require.Empty(t, stdout.String())
			require.NotContains(t, stderr.String(), "setup is complete")
			require.Equal(t, before, treeBytes(t, root))
		})
	}
}

type planChangeWriter struct {
	bytes.Buffer
	change func()
}

func (writer *planChangeWriter) Write(value []byte) (int, error) {
	n, err := writer.Buffer.Write(value)
	if writer.change != nil {
		change := writer.change
		writer.change = nil
		change()
	}
	return n, err
}

func TestRestoreRevalidatesCurrentTrafficAfterDisplayingPlan(t *testing.T) {
	for _, state := range []string{"valid", "corrupt", "missing"} {
		t.Run(state, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "gateway")
			command := newTestRootCmd(t)
			command.SetOut(new(bytes.Buffer))
			command.SetErr(new(bytes.Buffer))
			command.SetArgs([]string{"init", "--data-dir", root, "--confirm"})
			require.NoError(t, command.ExecuteContext(t.Context()))
			owner, err := gatewaypaths.Acquire(root)
			require.NoError(t, err)
			store, err := storage.Open(t.Context(), owner)
			require.NoError(t, err)
			identity, err := store.Identity(t.Context())
			require.NoError(t, err)
			identity.TrafficGeneration, err = store.SelectedTraffic(t.Context())
			require.NoError(t, err)
			traffic, err := invocation.OpenTraffic(t.Context(), owner, identity.InstallationID, identity.TrafficGeneration, invocation.DefaultTrafficConfig())
			require.NoError(t, err)
			manager, err := backup.New(backup.Options{Traffic: traffic, Store: store, Layout: owner.Layout(), Clock: systemClock{}, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x55}, 128))})
			require.NoError(t, err)
			artifact, _, err := manager.Create(t.Context(), "authority", "restore-plan")
			require.NoError(t, err)
			require.NoError(t, traffic.Close())
			require.NoError(t, store.Close())
			require.NoError(t, owner.Close())
			path := filepath.Join(root, "traffic-"+identity.TrafficGeneration+".db")
			switch state {
			case "missing":
				require.NoError(t, os.Remove(path))
			case "corrupt":
				require.NoError(t, os.WriteFile(path, []byte("damaged before plan"), 0o600))
			}
			before := treeBytes(t, root)
			secret := filepath.Join(t.TempDir(), "replacement")
			args := []string{"maintenance", "restore-backup", artifact.ID, "--data-dir", root, "--secret-output", secret, "--json"}
			stdout := new(bytes.Buffer)
			command = newTestRootCmd(t)
			command.SetOut(stdout)
			command.SetErr(new(bytes.Buffer))
			command.SetArgs(append(append([]string{}, args...), "--dry-run"))
			require.NoError(t, command.ExecuteContext(t.Context()))
			if state != "valid" {
				require.Contains(t, stdout.String(), "Current traffic integrity is not verified")
			}
			require.Equal(t, before, treeBytes(t, root))
			stderr := &planChangeWriter{change: func() {
				require.NoError(t, os.WriteFile(path, []byte("material change after plan"), 0o600))
			}}
			stdout.Reset()
			command = newTestRootCmd(t)
			command.SetOut(stdout)
			command.SetErr(stderr)
			command.SetArgs(append(args, "--confirm"))
			require.Error(t, command.ExecuteContext(t.Context()))
			require.Contains(t, stderr.String(), "plan_changed")
			require.Empty(t, stdout.String())
			before[path] = "material change after plan"
			require.Equal(t, before, treeBytes(t, root))
			_, err = os.Stat(secret)
			require.ErrorIs(t, err, os.ErrNotExist)
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
