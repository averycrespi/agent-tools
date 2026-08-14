//go:build e2e

package e2e_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProvisionScriptUsesOnlyAgentTokenAndConverges(t *testing.T) {
	home := t.TempDir()
	tokenPath := filepath.Join(home, ".config", "mcp-broker", "agent-token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o750); err != nil {
		t.Fatalf("create token directory: %v", err)
	}
	const tokenValue = "sandbox-agent-value"
	if err := os.WriteFile(tokenPath, []byte(tokenValue+"\n"), 0o600); err != nil {
		t.Fatalf("write agent token: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("export EXISTING=value"), 0o600); err != nil {
		t.Fatalf("seed bashrc: %v", err)
	}

	script := filepath.Join(mustFindModuleRoot(), "mcp-broker", "examples", "provision", "configure-mcp-broker.sh")
	run := func() string {
		command := exec.Command("bash", script)
		command.Env = append(os.Environ(), "HOME="+home)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("run provisioning script: %v\n%s", err, output)
		}
		bashrc, err := os.ReadFile(filepath.Join(home, ".bashrc"))
		if err != nil {
			t.Fatalf("read bashrc: %v", err)
		}
		return string(bashrc)
	}

	first := run()
	second := run()
	if first != second {
		t.Fatal("a second provisioning run changed the managed shell configuration")
	}
	if strings.Count(second, "# >>> mcp-broker >>>") != 1 {
		t.Fatal("provisioning did not leave exactly one managed block")
	}
	if !strings.Contains(second, ".config/mcp-broker/agent-token") {
		t.Fatal("managed block does not read the canonical agent token")
	}
	if !strings.Contains(second, `export MCP_BROKER_ENDPOINT="http://host.lima.internal:8200"`) {
		t.Fatal("managed block does not export the broker base URL")
	}
	if strings.Contains(second, "admin-token") {
		t.Fatal("managed block references the dashboard-only credential")
	}
	if strings.Contains(second, tokenValue) {
		t.Fatal("managed block embeds the agent credential instead of reading it at shell startup")
	}

	source, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("read provisioning script: %v", err)
	}
	if strings.Contains(string(source), "admin-token") {
		t.Fatal("provisioning script references the dashboard-only credential")
	}
	if strings.Contains(string(source), "auth-token") {
		t.Fatal("provisioning script still references the migration-only legacy path")
	}
}
