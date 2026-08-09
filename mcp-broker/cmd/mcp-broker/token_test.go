package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/auth"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestTokenRoleCommandsRequireExplicitValidRole(t *testing.T) {
	for _, command := range []*cobraCommandCase{
		{name: "show", args: tokenShowCmd.Args},
		{name: "rotate", args: tokenRotateCmd.Args},
	} {
		t.Run(command.name+" missing", func(t *testing.T) {
			err := command.args(nil, nil)
			require.ErrorContains(t, err, "token "+command.name)
			require.ErrorContains(t, err, "<agent|admin>")
		})
		t.Run(command.name+" extra", func(t *testing.T) {
			err := command.args(nil, []string{"agent", "extra"})
			require.ErrorContains(t, err, "token "+command.name)
			require.ErrorContains(t, err, "<agent|admin>")
		})
		t.Run(command.name+" invalid", func(t *testing.T) {
			tokenShapedArgument := strings.Repeat("a", 64)
			err := command.args(nil, []string{tokenShapedArgument})
			require.ErrorContains(t, err, "invalid token role")
			require.ErrorContains(t, err, "agent or admin")
			require.NotContains(t, err.Error(), tokenShapedArgument)
		})
		for _, role := range []string{"agent", "admin"} {
			t.Run(command.name+" "+role, func(t *testing.T) {
				require.NoError(t, command.args(nil, []string{role}))
			})
		}
	}
}

type cobraCommandCase struct {
	name string
	args func(*cobra.Command, []string) error
}

func TestTokenShowWritesOnlySelectedRawToken(t *testing.T) {
	setTokenTestConfigHome(t)
	tokens, err := auth.EnsureTokenSet(auth.DefaultTokenPaths())
	require.NoError(t, err)

	for _, tt := range []struct {
		role string
		want string
	}{
		{role: "agent", want: tokens.Agent},
		{role: "admin", want: tokens.Admin},
	} {
		t.Run(tt.role, func(t *testing.T) {
			var output bytes.Buffer
			tokenShowCmd.SetOut(&output)
			require.NoError(t, tokenShowCmd.RunE(tokenShowCmd, []string{tt.role}))
			if output.String() != tt.want+"\n" {
				t.Fatal("token show did not print exactly the selected credential and one newline")
			}
		})
	}
}

func TestTokenRotateChangesOnlySelectedFileWithoutPrintingCredential(t *testing.T) {
	setTokenTestConfigHome(t)
	before, err := auth.EnsureTokenSet(auth.DefaultTokenPaths())
	require.NoError(t, err)

	var output bytes.Buffer
	tokenRotateCmd.SetOut(&output)
	require.NoError(t, tokenRotateCmd.RunE(tokenRotateCmd, []string{"agent"}))
	afterAgent, err := auth.EnsureTokenSet(auth.DefaultTokenPaths())
	require.NoError(t, err)
	if before.Agent == afterAgent.Agent {
		t.Fatal("agent rotation did not change the agent credential")
	}
	if before.Admin != afterAgent.Admin {
		t.Fatal("agent rotation changed the admin credential")
	}
	if strings.Contains(output.String(), afterAgent.Agent) {
		t.Fatal("agent rotation printed the raw replacement credential")
	}
	require.Contains(t, output.String(), "agent token")
	require.Contains(t, output.String(), "SIGHUP")
	require.Contains(t, output.String(), "re-provision")

	output.Reset()
	require.NoError(t, tokenRotateCmd.RunE(tokenRotateCmd, []string{"admin"}))
	afterAdmin, err := auth.EnsureTokenSet(auth.DefaultTokenPaths())
	require.NoError(t, err)
	if afterAgent.Agent != afterAdmin.Agent {
		t.Fatal("admin rotation changed the agent credential")
	}
	if afterAgent.Admin == afterAdmin.Admin {
		t.Fatal("admin rotation did not change the admin credential")
	}
	if strings.Contains(output.String(), afterAdmin.Admin) {
		t.Fatal("admin rotation printed the raw replacement credential")
	}
	require.Contains(t, output.String(), "admin token")
	require.Contains(t, output.String(), "SIGHUP")
	require.Contains(t, output.String(), "reopen")
}

func TestTokenShowMigratesLegacyValueAsAgent(t *testing.T) {
	dir := setTokenTestConfigHome(t)
	legacy := filepath.Join(dir, "mcp-broker", "auth-token")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o750))
	value := strings.Repeat("a", 64)
	require.NoError(t, os.WriteFile(legacy, []byte(value), 0o600))
	var output bytes.Buffer
	tokenShowCmd.SetOut(&output)

	require.NoError(t, tokenShowCmd.RunE(tokenShowCmd, []string{"agent"}))

	require.Equal(t, value+"\n", output.String())
	require.NoFileExists(t, legacy)
}

func setTokenTestConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}
