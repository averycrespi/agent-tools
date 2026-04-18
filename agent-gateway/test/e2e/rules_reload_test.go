//go:build e2e

package e2e_test

// TestRuleReloadHotSwap is the §12 M3 acceptance gate.
//
// Scenario:
//  1. Start daemon with a rule file referencing ${secrets.x} (secret not set).
//  2. Run "rules reload" — must exit 0.
//  3. Fire a request that matches the rule.
//  4. Assert the audit row has injection='failed', error='secret_unresolved'.
//  5. Overwrite the rule file with invalid HCL; run "rules reload" — must exit non-zero.
//  6. Assert the previous (valid) ruleset stays live: the same request still
//     matches the old rule (the daemon does not reload on parse failure).
//
// The test is skipped until Tasks 21 (engine wired to proxy), 22–25 (secrets +
// injection) and 27 (audit queries) are complete. Task 27 removes the skip.

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRuleReloadHotSwap(t *testing.T) {
	t.Skip("requires M4")

	// -------------------------------------------------------------------------
	// Step 1: spin up the stack with a rule that references ${secrets.x}.
	// The secret is intentionally absent so injection must fail soft.
	// -------------------------------------------------------------------------
	stack := newTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "upstream ok")
	}))

	// Write a rules file into XDG_DATA_HOME so the daemon can pick it up.
	// The rule matches any GET to the mock upstream host and declares `allow`
	// with an inject block that references the undefined secret.
	//
	// NOTE: newTestStack uses t.TempDir() for XDG_DATA_HOME; the daemon writes
	// its data there. We need the corresponding config dir to place the rule.
	// Task 21 will wire the rules directory via config, at which point the
	// path constants here should be updated to use paths.RulesDir()-equivalent
	// logic against the temp XDG dirs.
	rulesDir := filepath.Join(t.TempDir(), "agent-gateway", "rules.d")
	if err := os.MkdirAll(rulesDir, 0o750); err != nil {
		t.Fatalf("create rules dir: %v", err)
	}
	ruleFile := filepath.Join(rulesDir, "test.hcl")
	validRule := fmt.Sprintf(`
rule "inject-x" {
  verdict = "allow"
  match {
    host = %q
  }
  inject {
    set_header = {
      "Authorization" = "Bearer ${secrets.x}"
    }
  }
}
`, stack.UpstreamURL)
	if err := os.WriteFile(ruleFile, []byte(validRule), 0o600); err != nil {
		t.Fatalf("write rule file: %v", err)
	}

	// -------------------------------------------------------------------------
	// Step 2: reload — must succeed (exit 0).
	// -------------------------------------------------------------------------
	reloadCmd := exec.Command(gatewayBinary, "rules", "reload")
	reloadCmd.Stdout = os.Stdout
	reloadCmd.Stderr = os.Stderr
	if err := reloadCmd.Run(); err != nil {
		t.Fatalf("rules reload (valid rule): %v", err)
	}

	// -------------------------------------------------------------------------
	// Step 3: fire a matching request through the proxy.
	// -------------------------------------------------------------------------
	resp, err := stack.AgentClient.Get(stack.UpstreamURL)
	if err != nil {
		t.Fatalf("GET via proxy: %v", err)
	}
	resp.Body.Close()

	// -------------------------------------------------------------------------
	// Step 4: assert audit row has injection='failed', error='secret_unresolved'.
	//
	// TODO (Task 27): query the audit DB directly or via the dashboard API.
	// Expected:
	//   SELECT injection, error FROM requests ORDER BY ts DESC LIMIT 1;
	//   → injection='failed', error='secret_unresolved'
	// -------------------------------------------------------------------------
	t.Log("TODO: assert audit row injection='failed', error='secret_unresolved' (Task 27)")

	// -------------------------------------------------------------------------
	// Step 5: replace rule file with invalid HCL; reload must exit non-zero.
	// -------------------------------------------------------------------------
	if err := os.WriteFile(ruleFile, []byte("this is not { valid hcl"), 0o600); err != nil {
		t.Fatalf("overwrite rule file with invalid HCL: %v", err)
	}
	badReload := exec.Command(gatewayBinary, "rules", "reload")
	badReload.Stdout = os.Stdout
	badReload.Stderr = os.Stderr
	if err := badReload.Run(); err == nil {
		t.Fatal("rules reload with invalid HCL: expected non-zero exit, got 0")
	}

	// -------------------------------------------------------------------------
	// Step 6: previous ruleset stays live — the same request still reaches the
	// upstream (old rule allows it).
	// -------------------------------------------------------------------------
	resp2, err := stack.AgentClient.Get(stack.UpstreamURL)
	if err != nil {
		t.Fatalf("GET after bad reload: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from still-live old ruleset, got %d", resp2.StatusCode)
	}

	// TODO (Task 27): assert audit row for this second request also has
	// injection='failed', error='secret_unresolved' (old rule still in effect).
	t.Log("TODO: assert second request also matches old rule (Task 27)")
}
