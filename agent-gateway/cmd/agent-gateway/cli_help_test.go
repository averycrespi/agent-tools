package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdministrationInputModeCommands(t *testing.T) {
	contents, err := os.ReadFile("../../docs/operators/administration.md")
	require.NoError(t, err)
	root := newRootCmd()
	commands := 0
	for _, line := range strings.Split(string(contents), "\n") {
		if !strings.HasPrefix(line, "| Direct flags") && !strings.HasPrefix(line, "| Strict") {
			continue
		}
		cells := strings.Split(line, "|")
		require.GreaterOrEqual(t, len(cells), 4)
		for _, match := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(cells[2], -1) {
			command, remaining, err := root.Find(strings.Fields(match[1]))
			require.NoError(t, err, match[1])
			assert.Empty(t, remaining, match[1])
			assert.Equal(t, "agent-gateway "+match[1], command.CommandPath())
			commands++
		}
	}
	require.Positive(t, commands, "input-mode table must contain executable command paths")
	command, remaining, err := root.Find([]string{"server", "create"})
	require.True(t, err != nil || len(remaining) != 0 || command.CommandPath() != "agent-gateway server create", "retired top-level command must not become an alias")
}

func TestCLIHelpTree(t *testing.T) {
	root := newRootCmd()
	assert.Equal(t, "agent-gateway", root.Name())
	assert.Contains(t, root.Long, "Agent Gateway")
	assert.Contains(t, root.Long, "Only agent-gateway is published")
	assert.Contains(t, root.Example, "agent-gateway initialize")
	assert.Contains(t, root.Example, "agent-gateway serve")
	assert.Contains(t, root.Example, "agent-gateway status")
	initialize, _, err := root.Find([]string{"initialize"})
	require.NoError(t, err)
	reset, _, err := root.Find([]string{"admin", "reset"})
	require.NoError(t, err)
	assert.NotEqual(t, initialize.Short, reset.Short)
	credentialGet, _, err := root.Find([]string{"admin", "credential", "get"})
	require.NoError(t, err)
	assert.Contains(t, credentialGet.Short, "by ID")
	invocationGet, _, err := root.Find([]string{"mcp", "invocation", "get"})
	require.NoError(t, err)
	assert.Contains(t, invocationGet.Short, "JSON")

	var snapshot strings.Builder
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		assert.NotEmpty(t, command.Short, command.CommandPath())
		assert.NotContains(t, command.Example, "mcp-gateway ", command.CommandPath())
		assert.NotContains(t, command.Short, "Online Gateway control commands", command.CommandPath())
		assert.NotContains(t, command.Short, "Operate the local Gateway through its public control API", command.CommandPath())
		output := new(bytes.Buffer)
		command.SetOut(output)
		require.NoError(t, command.Help(), command.CommandPath())
		fmt.Fprintf(&snapshot, "[%s]\n%s\n", command.CommandPath(), output.String())
		children := append([]*cobra.Command(nil), command.Commands()...)
		sort.Slice(children, func(left, right int) bool { return children[left].Name() < children[right].Name() })
		for _, child := range children {
			walk(child)
		}
	}
	walk(root)
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(snapshot.String())))
	assert.Equal(t, "sha256:8048df78bb0524ea40bc4c16e1110604f3b950a496b22ca425b7fb5c296e74a6", digest)
}

func TestCLIOAuthCompatibilityHelp(t *testing.T) {
	for _, path := range [][]string{{"mcp", "server", "create"}, {"mcp", "server", "update"}} {
		command, _, err := newRootCmd().Find(path)
		require.NoError(t, err)
		for _, expected := range []string{"callback_uri", "http://localhost:3118/callback", "auth_server_metadata_url", "scopes", "null", "[]", "request_offline_access", "temporary callback-only"} {
			assert.Contains(t, command.Long, expected)
		}
	}
}

func TestCLIPrincipalHelpDistinguishesMCPPolicy(t *testing.T) {
	for _, action := range []string{"create", "update"} {
		command, _, err := newRootCmd().Find([]string{"principal", action})
		require.NoError(t, err)
		output := new(bytes.Buffer)
		command.SetOut(output)
		require.NoError(t, command.Help())
		for _, expected := range []string{"shared administration", "MCP discovery", "grants no access", "MCP grants remain authoritative", "requestable, allowed-only, or all"} {
			assert.Contains(t, output.String(), expected, action)
		}
		assert.Empty(t, command.Flags().Lookup("visibility").DefValue)
		if action == "create" {
			for _, expected := range []string{"Default Gateway access", "six fixed MCP self-service tools", "not downstream tools or future protocols", "principal and default_grant", "Issue the credential separately"} {
				assert.Contains(t, output.String(), expected)
			}
		}
	}
}

func TestCLICredentialHelpExplainsOneTimeOutput(t *testing.T) {
	for _, test := range []struct {
		path     []string
		expected []string
	}{
		{path: []string{"admin", "credential", "create"}, expected: []string{"0600", "controlling terminal", "cannot be recovered", "stdout or JSON"}},
		{path: []string{"admin", "credential", "rotate"}, expected: []string{"required", "0600", "cannot be recovered", "default bearer"}},
		{path: []string{"principal", "credential", "issue"}, expected: []string{"0600", "controlling terminal", "cannot be recovered", "stdout or JSON"}},
		{path: []string{"principal", "credential", "rotate"}, expected: []string{"0600", "controlling terminal", "cannot be recovered", "stdout or JSON"}},
		{path: []string{"initialize"}, expected: []string{"0600", "cannot be recovered"}},
		{path: []string{"admin", "reset"}, expected: []string{"0600", "cannot be recovered"}},
		{path: []string{"backup", "restore"}, expected: []string{"0600", "cannot be recovered"}},
		{path: []string{"storage", "verify"}, expected: []string{"recognized recovery", "without replacing"}},
	} {
		root := newRootCmd()
		command, remaining, err := root.Find(test.path)
		require.NoError(t, err)
		assert.Empty(t, remaining)
		output := new(bytes.Buffer)
		command.SetOut(output)
		require.NoError(t, command.Help())
		for _, expected := range test.expected {
			assert.Contains(t, output.String(), expected, strings.Join(test.path, " "))
		}
	}
}

func testDocumentationCommandHelpProjection(t *testing.T) {
	root := newRootCmd()
	manifest := contract.DocumentationCommandManifest()
	manifestPaths := make([]string, 0, len(manifest))
	for _, family := range manifest {
		manifestPaths = append(manifestPaths, family.CommandPath)
		command, remaining, err := root.Find(strings.Fields(family.CommandPath))
		require.NoError(t, err, family.ID)
		assert.Empty(t, remaining, family.ID)
		assert.Equal(t, "agent-gateway "+family.CommandPath, command.CommandPath(), family.ID)
		// The manifest uses only the canonical executable and grammar.
		assert.Equal(t, "agent-gateway "+family.CommandPath+" --help", family.HelpInvocation, family.ID)
		output := new(bytes.Buffer)
		command.SetOut(output)
		require.NoError(t, command.Help(), family.ID)
		assert.NotEmpty(t, output.String(), family.ID)
	}
	sort.Strings(manifestPaths)

	for _, command := range root.Commands() {
		if command.Name() == "completion" || command.Name() == "help" {
			continue
		}
		covered := false
		for _, path := range manifestPaths {
			if path == command.Name() || strings.HasPrefix(path, command.Name()+" ") {
				covered = true
				break
			}
		}
		assert.True(t, covered, command.Name())
	}
}

func TestCLIUnknownCommandsAreInputErrors(t *testing.T) {
	for _, args := range [][]string{
		{"definitely-unknown"},
		{"--definitely-invalid"},
		{"admin", "definitely-unknown"},
		{"admin", "--definitely-invalid"},
		{"admin", "credential", "definitely-unknown"},
	} {
		command := newRootCmd()
		stdout := new(bytes.Buffer)
		stderr := new(bytes.Buffer)
		command.SetOut(stdout)
		command.SetErr(stderr)
		command.SetArgs(args)

		err := command.ExecuteContext(context.Background())

		require.Error(t, err, strings.Join(args, " "))
		assert.Equal(t, 2, commandExitCode(err), strings.Join(args, " "))
		var problem *controlclient.OnlineError
		require.ErrorAs(t, err, &problem, strings.Join(args, " "))
		assert.Equal(t, "client_invalid_input", problem.Code, strings.Join(args, " "))
		assert.Empty(t, stdout.String(), strings.Join(args, " "))
		assert.Contains(t, stderr.String(), "--help", strings.Join(args, " "))
	}
}

func testCLICommandErrors(t *testing.T) {
	requiredFlags := map[string][]string{
		"admin credential rotate OLD_CREDENTIAL_ID --secret-output NEW_PATH": {"secret-output"},
		"mcp server create --file PATH":                                      {"file"},
		"mcp server credential replace ID --file PATH [--etag ETAG]":         {"file"},
	}

	offlineCases := []struct {
		name  string
		args  []string
		usage string
	}{
		{name: "initialize arguments", args: []string{"initialize", "EXTRA", "--json"}, usage: "agent-gateway initialize"},
		{name: "initialize flag", args: []string{"initialize", "--json", "--definitely-invalid"}, usage: "agent-gateway initialize"},
		{name: "admin reset arguments", args: []string{"admin", "reset", "EXTRA", "--json"}, usage: "agent-gateway admin reset --secret-output NEW_PATH"},
		{name: "admin reset required flag", args: []string{"admin", "reset", "--json"}, usage: "agent-gateway admin reset --secret-output NEW_PATH"},
		{name: "admin reset flag", args: []string{"admin", "reset", "--json", "--definitely-invalid"}, usage: "agent-gateway admin reset --secret-output NEW_PATH"},
		{name: "restore arguments", args: []string{"backup", "restore", "--json"}, usage: "agent-gateway backup restore BACKUP_ID"},
		{name: "restore required flag", args: []string{"backup", "restore", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "--json"}, usage: "agent-gateway backup restore BACKUP_ID --secret-output NEW_PATH"},
		{name: "restore flag", args: []string{"backup", "restore", "--json", "--definitely-invalid"}, usage: "agent-gateway backup restore BACKUP_ID"},
		{name: "verify arguments", args: []string{"storage", "verify", "unexpected", "--json"}, usage: "agent-gateway storage verify"},
		{name: "verify secret flag", args: []string{"storage", "verify", "--json", "--secret-output", "unused"}, usage: "agent-gateway storage verify"},
		{name: "serve arguments", args: []string{"serve", "EXTRA", "--json"}, usage: "agent-gateway serve"},
		{name: "serve flag", args: []string{"serve", "--json", "--definitely-invalid"}, usage: "agent-gateway serve"},
	}
	for _, test := range offlineCases {
		t.Run("offline/"+test.name, func(t *testing.T) {
			problem := executeCLIProblem(t, test.args...)
			assert.Equal(t, "client_invalid_input", problem.Code)
			assert.Contains(t, problem.Title, "Usage: "+test.usage)
		})
	}

	for _, spec := range onlineCommandSpecs() {
		spec := spec
		assert.Equal(t, requiredFlags[spec.ManifestUse], spec.RequiredFlags, spec.ManifestUse)
		t.Run("arguments/"+strings.Join(spec.Path, "_"), func(t *testing.T) {
			args := append([]string(nil), spec.Path...)
			positionals := positionalArguments(spec.Use)
			if len(positionals) == 0 {
				args = append(args, "EXTRA")
			} else {
				for range positionals[:len(positionals)-1] {
					args = append(args, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
				}
			}
			args = append(args, "--json")
			problem := executeCLIProblem(t, args...)
			assert.Equal(t, "client_invalid_input", problem.Code)
			assert.Contains(t, problem.Title, "Usage: agent-gateway "+spec.ManifestUse)
		})

		t.Run("invalid_flag/"+strings.Join(spec.Path, "_"), func(t *testing.T) {
			args := append(append([]string(nil), spec.Path...), "--json", "--definitely-invalid")
			problem := executeCLIProblem(t, args...)
			assert.Equal(t, "client_invalid_input", problem.Code)
			assert.Contains(t, problem.Title, "flag")
			assert.Contains(t, problem.Title, "Usage: agent-gateway "+spec.ManifestUse)
		})

		flags := requiredFlags[spec.ManifestUse]
		for _, omitted := range flags {
			omitted := omitted
			t.Run("required_flag/"+strings.Join(spec.Path, "_")+"/"+omitted, func(t *testing.T) {
				args := append([]string(nil), spec.Path...)
				for range positionalArguments(spec.Use) {
					args = append(args, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
				}
				for _, flag := range flags {
					if flag != omitted {
						args = append(args, "--"+flag, "present")
					}
				}
				args = append(args, "--json")
				problem := executeCLIProblem(t, args...)
				assert.Equal(t, "client_invalid_input", problem.Code)
				assert.Contains(t, problem.Title, "--"+omitted)
				assert.Contains(t, problem.Title, "Usage: agent-gateway "+spec.ManifestUse)
			})
		}
	}
}

func positionalArguments(use string) []string {
	fields := strings.Fields(use)
	result := make([]string, 0)
	for _, field := range fields[1:] {
		if field == strings.ToUpper(field) {
			result = append(result, field)
		}
	}
	return result
}

func executeCLIProblem(t *testing.T, args ...string) struct {
	Code  string `json:"code"`
	Title string `json:"title"`
} {
	t.Helper()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	command := newRootCmd()
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs(args)
	err := command.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Equal(t, 2, commandExitCode(err))
	assert.Empty(t, stdout.String())
	var problem struct {
		Code  string `json:"code"`
		Title string `json:"title"`
	}
	require.NoError(t, json.Unmarshal(stderr.Bytes(), &problem), stderr.String())
	assert.NotEqual(t, "invalid_command", problem.Code)
	return problem
}
