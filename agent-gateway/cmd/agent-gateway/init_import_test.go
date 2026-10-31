package main

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/configimport"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/stretchr/testify/require"
)

const cliImportDocument = `{"format":"agent-gateway-config","version":1,"agents":[],"mcp":{"servers":[],"grants":[]},"http":{"credentials":[],"grants":[]},"git":{"credentials":[],"repositories":[],"grants":[],"routing_origins":[]}}`

func TestInitFromValidatesBeforeFilesAndRequiresDefaultNoConsent(t *testing.T) {
	for _, scenario := range []string{"invalid", "oversized", "declined", "from-with-resume", "nonempty", "symlink", "empty-from"} {
		t.Run(scenario, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "gateway")
			source := filepath.Join(t.TempDir(), "config.json")
			data := cliImportDocument
			if scenario == "invalid" {
				data = strings.Replace(data, `"version":1`, `"version":999`, 1)
			}
			require.NoError(t, os.WriteFile(source, []byte(data), 0600))
			args := []string{"init", "--from", source, "--confirm", "--data-dir", root}
			switch scenario {
			case "oversized":
				require.NoError(t, os.Truncate(source, contract.ConfigExportMaxBytes+1))
			case "declined":
				args = []string{"init", "--from", source, "--data-dir", root}
			case "from-with-resume":
				args = append(args, "--resume")
			case "empty-from":
				args = []string{"init", "--from=", "--confirm", "--data-dir", root}
			case "nonempty":
				require.NoError(t, os.Mkdir(root, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(root, "keep"), []byte("unrelated"), 0600))
			case "symlink":
				link := source + ".link"
				require.NoError(t, os.Symlink(source, link))
				args[2] = link
			}
			command := newTestRootCmd(t)
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			command.SetOut(stdout)
			command.SetErr(stderr)
			command.SetIn(strings.NewReader("n\n"))
			command.SetArgs(args)
			require.Error(t, command.ExecuteContext(t.Context()))
			if scenario == "nonempty" {
				entries, err := os.ReadDir(root)
				require.NoError(t, err)
				require.Len(t, entries, 1)
				require.Equal(t, "keep", entries[0].Name())
			} else {
				_, err := os.Lstat(root)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
			if scenario == "declined" {
				require.Contains(t, stderr.String(), "Import: 0 agents")
				require.Contains(t, stderr.String(), "All secrets remain unset")
			}
		})
	}
}

func TestInitFromCreatesFreshInstallationAndKeepsSourceUnchanged(t *testing.T) {
	source := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(source, []byte(cliImportDocument), 0600))
	root := filepath.Join(t.TempDir(), "gateway")
	command := newTestRootCmd(t)
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs([]string{"init", "--from", source, "--confirm", "--json", "--data-dir", root})
	require.NoError(t, command.ExecuteContext(t.Context()))
	require.Contains(t, stdout.String(), `"changed":true`)
	require.Contains(t, stdout.String(), `"setup_tasks"`)
	require.NotContains(t, stdout.String(), "mgw_admin_")
	actual, err := os.ReadFile(source)
	require.NoError(t, err)
	require.Equal(t, cliImportDocument, string(actual))
	layout, err := gatewaypaths.Resolve(root)
	require.NoError(t, err)
	plan, complete, err := gatewaypaths.ReadInitialization(layout)
	require.NoError(t, err)
	require.True(t, complete)
	require.NotEmpty(t, plan.ImportDigest)
}

func TestInitFromResumesPrivateInputAfterSourceDeletionAndNeverMerges(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	source := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(source, []byte(cliImportDocument), 0600))
	input, err := configimport.Decode([]byte(cliImportDocument))
	require.NoError(t, err)
	owner, err := gatewaypaths.AcquireInitialization(root)
	require.NoError(t, err)
	installation, err := admin.NewID(time.Now(), rand.Reader)
	require.NoError(t, err)
	traffic, err := admin.NewID(time.Now(), rand.Reader)
	require.NoError(t, err)
	plan := gatewaypaths.InitializationPlan{Version: 1, Root: root, Bearer: owner.Layout().AdminBearer, Installation: installation, Traffic: traffic, ImportDigest: input.Digest}
	require.NoError(t, gatewaypaths.BeginInitialization(owner, plan))
	require.NoError(t, gatewaypaths.RetainInitializationInput(owner, plan, input.Bytes))
	require.NoError(t, owner.Close())
	require.NoError(t, os.Remove(source))
	command := newTestRootCmd(t)
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs([]string{"init", "--resume", "--confirm", "--data-dir", root})
	require.NoError(t, command.ExecuteContext(t.Context()))
	require.Contains(t, stdout.String(), "Imported setup tasks:")
	require.Contains(t, stdout.String(), "agent credential issue")
	require.Contains(t, stdout.String(), "operation start ID --kind reload")
	require.Contains(t, stdout.String(), "source CA trust does not transfer")
	_, err = os.Stat(filepath.Join(root, gatewaypaths.InitializationName, "input"))
	require.ErrorIs(t, err, os.ErrNotExist)
	before := treeBytes(t, root)
	command = newTestRootCmd(t)
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"init", "--resume", "--confirm", "--data-dir", root})
	require.NoError(t, command.ExecuteContext(t.Context()))
	require.Equal(t, before, treeBytes(t, root))
	require.NoError(t, os.WriteFile(source, []byte(cliImportDocument), 0600))
	command = newTestRootCmd(t)
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"init", "--from", source, "--confirm", "--data-dir", root})
	require.Error(t, command.ExecuteContext(t.Context()))
	require.Equal(t, before, treeBytes(t, root))
}
