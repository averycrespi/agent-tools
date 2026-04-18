//go:build e2e

package e2e_test

// TestRuleReloadHotSwap is the §12 M3 acceptance gate.
//
// Scenario:
//  1. Start daemon with a rule file referencing ${secrets.x} (secret not set).
//  2. Run "rules reload" — must exit 0.
//  3. Fire a request that matches the rule.
//  4. Assert fail-soft: upstream received the agent's original dummy header,
//     NOT the injected value (secret_unresolved → pass-through).
//  5. Overwrite the rule file with invalid HCL; run "rules reload" — exits 0
//     because the CLI only sends SIGHUP; the daemon silently drops the bad
//     reload and keeps the previous ruleset live.
//  6. Assert the previous (valid) ruleset stays live: the same request still
//     matches the old rule (the daemon does not swap rules on parse failure).
//
// TODO (Task 33): add audit-row assertions:
//   SELECT injection, error FROM requests ORDER BY ts DESC LIMIT 1
//   → injection='failed', error='secret_unresolved'

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"os"
)

func TestRuleReloadHotSwap(t *testing.T) {
	// -------------------------------------------------------------------------
	// Step 1: spin up the stack. The mock upstream echoes the Authorization
	// header it received so we can assert fail-soft behaviour.
	// -------------------------------------------------------------------------
	stack := newTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		w.Header().Set("X-Saw-Auth", auth)
		fmt.Fprint(w, auth)
	}))

	// Extract hostname for the match block (IP only, no port).
	upstreamURL, err := url.Parse(stack.UpstreamURL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	upstreamHost := upstreamURL.Hostname()

	// Write a rule that references ${secrets.x}. The secret is intentionally
	// absent so injection must fail soft (pass-through).
	ruleContent := fmt.Sprintf(`
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
`, upstreamHost)
	stack.writeRule(t, "test.hcl", ruleContent)

	// -------------------------------------------------------------------------
	// Step 2: reload — must succeed (exit 0).
	// -------------------------------------------------------------------------
	stack.rulesReload(t)
	// Give the daemon a brief moment to apply the reload.
	time.Sleep(200 * time.Millisecond)

	// -------------------------------------------------------------------------
	// Step 3: fire a matching request through the proxy.
	// -------------------------------------------------------------------------
	req1, err := http.NewRequest(http.MethodGet, stack.UpstreamURL, nil)
	if err != nil {
		t.Fatalf("build request 1: %v", err)
	}
	req1.Header.Set("Authorization", "Bearer dummy")

	resp1, err := stack.AgentClient.Do(req1)
	if err != nil {
		t.Fatalf("GET via proxy: %v", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("request 1: unexpected status %d", resp1.StatusCode)
	}

	// -------------------------------------------------------------------------
	// Step 4: assert fail-soft — upstream must have seen the agent's original
	// "Bearer dummy", NOT a substituted value (secret is unresolved).
	// -------------------------------------------------------------------------
	const wantAuth = "Bearer dummy"
	if sawAuth := resp1.Header.Get("X-Saw-Auth"); sawAuth != wantAuth {
		t.Errorf("fail-soft: upstream saw Authorization %q, want %q", sawAuth, wantAuth)
	}
	if string(body1) != wantAuth {
		t.Errorf("fail-soft: response body %q, want %q", string(body1), wantAuth)
	}

	// TODO (Task 33): query audit DB for injection='failed', error='secret_unresolved':
	//   SELECT injection, error FROM requests ORDER BY ts DESC LIMIT 1;

	// -------------------------------------------------------------------------
	// Step 5: replace rule file with invalid HCL and trigger a reload.
	// "rules reload" sends SIGHUP regardless of rule validity (exit 0); the
	// daemon's SIGHUP handler detects the parse error, logs it, and keeps the
	// previous ruleset live.
	// -------------------------------------------------------------------------
	if err := os.WriteFile(stack.RulesDir+"/test.hcl", []byte("this is not { valid hcl"), 0o600); err != nil {
		t.Fatalf("overwrite rule file with invalid HCL: %v", err)
	}
	stack.rulesReload(t)
	// Give the daemon time to attempt (and fail) the reload.
	time.Sleep(200 * time.Millisecond)

	// -------------------------------------------------------------------------
	// Step 6: previous ruleset stays live — the same request still matches the
	// old rule (daemon rejects invalid reload and keeps existing rules).
	// -------------------------------------------------------------------------
	req2, err := http.NewRequest(http.MethodGet, stack.UpstreamURL, nil)
	if err != nil {
		t.Fatalf("build request 2: %v", err)
	}
	req2.Header.Set("Authorization", "Bearer dummy")

	resp2, err := stack.AgentClient.Do(req2)
	if err != nil {
		t.Fatalf("GET after bad reload: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from still-live old ruleset, got %d", resp2.StatusCode)
	}
	// Old rule still in effect — injection still fails soft, pass-through preserved.
	if sawAuth2 := resp2.Header.Get("X-Saw-Auth"); sawAuth2 != wantAuth {
		t.Errorf("after bad reload: upstream saw Authorization %q, want %q (old rule should still apply)", sawAuth2, wantAuth)
	}
	if string(body2) != wantAuth {
		t.Errorf("after bad reload: response body %q, want %q", string(body2), wantAuth)
	}

	// TODO (Task 33): assert audit row for this second request also has
	// injection='failed', error='secret_unresolved' (old rule still in effect).
}
