package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/http-broker/internal/auth"
	"github.com/spf13/cobra"
)

func TestTokenRoleCommandsRequireExplicitValidRole(t *testing.T) {
	for _, command := range []struct {
		name string
		args func(*cobra.Command, []string) error
	}{
		{name: "show", args: tokenShowCmd.Args},
		{name: "rotate", args: tokenRotateCmd.Args},
	} {
		t.Run(command.name+" missing", func(t *testing.T) {
			err := command.args(nil, nil)
			if err == nil || !strings.Contains(err.Error(), "token "+command.name) || !strings.Contains(err.Error(), "<agent|admin>") {
				t.Fatalf("missing-role error is not command-specific: %v", err)
			}
		})
		t.Run(command.name+" extra", func(t *testing.T) {
			err := command.args(nil, []string{"agent", "extra"})
			if err == nil || !strings.Contains(err.Error(), "token "+command.name) || !strings.Contains(err.Error(), "<agent|admin>") {
				t.Fatalf("extra-role error is not command-specific: %v", err)
			}
		})
		t.Run(command.name+" invalid", func(t *testing.T) {
			tokenShapedArgument := strings.Repeat("a", 64)
			err := command.args(nil, []string{tokenShapedArgument})
			if err == nil || !strings.Contains(err.Error(), "invalid token role") || !strings.Contains(err.Error(), "agent or admin") {
				t.Fatalf("invalid-role error is not explicit: %v", err)
			}
			if strings.Contains(err.Error(), tokenShapedArgument) {
				t.Fatal("invalid-role error disclosed the supplied argument")
			}
		})
		for _, role := range []string{"agent", "admin"} {
			if err := command.args(nil, []string{role}); err != nil {
				t.Fatalf("valid role %q: %v", role, err)
			}
		}
	}
}

func TestTokenShowWritesOnlySelectedRawValue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	tokens, err := auth.EnsureTokenSet(auth.DefaultTokenPaths())
	if err != nil {
		t.Fatalf("EnsureTokenSet: %v", err)
	}
	for _, tt := range []struct{ role, want string }{{"agent", tokens.Agent}, {"admin", tokens.Admin}} {
		var output bytes.Buffer
		tokenShowCmd.SetOut(&output)
		if err := tokenShowCmd.RunE(tokenShowCmd, []string{tt.role}); err != nil {
			t.Fatalf("show %s: %v", tt.role, err)
		}
		if output.String() != tt.want+"\n" {
			t.Fatal("token show did not print exactly the selected credential and one newline")
		}
	}
}

func TestTokenRotateChangesOnlySelectedRoleWithoutPrintingRawValue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	before, err := auth.EnsureTokenSet(auth.DefaultTokenPaths())
	if err != nil {
		t.Fatalf("EnsureTokenSet: %v", err)
	}
	var output bytes.Buffer
	tokenRotateCmd.SetOut(&output)
	if err := tokenRotateCmd.RunE(tokenRotateCmd, []string{"agent"}); err != nil {
		t.Fatalf("rotate agent: %v", err)
	}
	afterAgent, err := auth.EnsureTokenSet(auth.DefaultTokenPaths())
	if err != nil {
		t.Fatalf("load after agent rotation: %v", err)
	}
	if before.Agent == afterAgent.Agent || before.Admin != afterAgent.Admin {
		t.Fatal("agent rotation did not change exactly the agent credential")
	}
	if strings.Contains(output.String(), afterAgent.Agent) {
		t.Fatal("agent rotation printed the raw replacement credential")
	}
	for _, phrase := range []string{"agent token", "re-provision", "SIGHUP"} {
		if !strings.Contains(output.String(), phrase) {
			t.Fatalf("agent guidance does not contain %q", phrase)
		}
	}

	output.Reset()
	if err := tokenRotateCmd.RunE(tokenRotateCmd, []string{"admin"}); err != nil {
		t.Fatalf("rotate admin: %v", err)
	}
	afterAdmin, err := auth.EnsureTokenSet(auth.DefaultTokenPaths())
	if err != nil {
		t.Fatalf("load after admin rotation: %v", err)
	}
	if afterAgent.Agent != afterAdmin.Agent || afterAgent.Admin == afterAdmin.Admin {
		t.Fatal("admin rotation did not change exactly the admin credential")
	}
	if strings.Contains(output.String(), afterAdmin.Admin) {
		t.Fatal("admin rotation printed the raw replacement credential")
	}
	for _, phrase := range []string{"admin token", "reopen", "SIGHUP"} {
		if !strings.Contains(output.String(), phrase) {
			t.Fatalf("admin guidance does not contain %q", phrase)
		}
	}
}

func TestProxyCredentialAlwaysUsesAgentRoleAndTakesNoArguments(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	tokens, err := auth.EnsureTokenSet(auth.DefaultTokenPaths())
	if err != nil {
		t.Fatalf("EnsureTokenSet: %v", err)
	}
	if err := tokenProxyURLCmd.Args(tokenProxyURLCmd, []string{"agent"}); err == nil {
		t.Fatal("proxy-credential accepted an argument")
	}
	var output bytes.Buffer
	tokenProxyURLCmd.SetOut(&output)
	if err := tokenProxyURLCmd.RunE(tokenProxyURLCmd, nil); err != nil {
		t.Fatalf("proxy-credential: %v", err)
	}
	if output.String() != auth.ProxyCredential(tokens.Agent)+"\n" {
		t.Fatal("proxy-credential was not derived only from the agent credential")
	}
	if strings.Contains(output.String(), tokens.Admin) {
		t.Fatal("proxy-credential output contained the admin credential")
	}
}
