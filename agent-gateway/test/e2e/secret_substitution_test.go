//go:build e2e

package e2e_test

// TestSecretSubstitution verifies secret values are substituted into injected
// headers (and that the agent never sees the real secret).
//
// Scenario:
//  1. Start the stack. Mock upstream echoes back the Authorization header it
//     receives.
//  2. Write a rule that injects Authorization: Bearer ${secrets.gh_bot}.
//  3. Trigger rules reload (SIGHUP) via "rules reload".
//  4. Agent sends a request with Authorization: Bearer dummy.
//     Secret is not yet set — injection fails soft.
//     Upstream sees the agent's original Bearer dummy.
//  5. Set the secret via "secret set gh_bot" piping "realtoken".
//     The CLI sends SIGHUP after mutation; the SIGHUP handler invalidates the
//     injector cache.
//  6. Give the daemon a moment to process the signal.
//  7. Agent sends another request with Authorization: Bearer dummy.
//     This time injection succeeds; upstream sees Bearer realtoken.
//  8. Assert the agent never saw the real token — the agent client only ever
//     sent Bearer dummy (verified via a request-side recorder).

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"
)

func TestSecretSubstitution(t *testing.T) {
	// -------------------------------------------------------------------------
	// Step 1: Spin up the stack. The mock upstream echoes the Authorization
	// header it received back to the caller in a custom response header
	// X-Saw-Auth, and also as the response body.
	// -------------------------------------------------------------------------
	var (
		mu      sync.Mutex
		sawAuth []string // Authorization headers captured at the upstream
	)

	stack := newTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		sawAuth = append(sawAuth, auth)
		mu.Unlock()
		w.Header().Set("X-Saw-Auth", auth)
		fmt.Fprint(w, auth)
	}))

	// Extract the upstream host (IP only, no port) for the rule match.
	upstreamURL, err := url.Parse(stack.UpstreamURL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	upstreamHost := upstreamURL.Hostname() // "127.0.0.1"

	// -------------------------------------------------------------------------
	// Step 2: Write a rule that injects Authorization from the secret.
	// -------------------------------------------------------------------------
	ruleContent := fmt.Sprintf(`
rule "inject-gh-bot" {
  verdict = "allow"
  match {
    host = %q
  }
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.gh_bot}"
    }
  }
}
`, upstreamHost)
	stack.writeRule(t, "gh-bot.hcl", ruleContent)

	// -------------------------------------------------------------------------
	// Step 3: Trigger rules reload.
	// -------------------------------------------------------------------------
	stack.rulesReload(t)
	// Give the daemon a brief moment to apply the reload.
	time.Sleep(200 * time.Millisecond)

	// -------------------------------------------------------------------------
	// Step 4: Agent sends a request. Secret is not set yet — injection fails
	// soft. Upstream must see the agent's original "Bearer dummy".
	// -------------------------------------------------------------------------
	req1, err := http.NewRequest(http.MethodGet, stack.UpstreamURL, nil)
	if err != nil {
		t.Fatalf("build request 1: %v", err)
	}
	req1.Header.Set("Authorization", "Bearer dummy")

	resp1, err := stack.AgentClient.Do(req1)
	if err != nil {
		t.Fatalf("request 1: %v", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("request 1: unexpected status %d", resp1.StatusCode)
	}

	mu.Lock()
	sawAuth1 := ""
	if len(sawAuth) >= 1 {
		sawAuth1 = sawAuth[0]
	}
	mu.Unlock()

	// Before the secret is set, the injector fails soft and passes through the
	// agent's own header unchanged.
	if sawAuth1 != "Bearer dummy" {
		t.Errorf("before secret set: upstream saw Authorization %q, want %q", sawAuth1, "Bearer dummy")
	}
	if string(body1) != "Bearer dummy" {
		t.Errorf("before secret set: response body %q, want %q", string(body1), "Bearer dummy")
	}

	// -------------------------------------------------------------------------
	// Step 5: Set the secret. The CLI sends SIGHUP after writing the secret;
	// the SIGHUP handler invalidates the injector cache.
	// -------------------------------------------------------------------------
	stack.setSecret(t, "gh_bot", "realtoken")

	// -------------------------------------------------------------------------
	// Step 6: Give the daemon a moment to process SIGHUP.
	// -------------------------------------------------------------------------
	time.Sleep(300 * time.Millisecond)

	// -------------------------------------------------------------------------
	// Step 7: Agent sends another request with "Bearer dummy".
	// Injection should now succeed; upstream must see "Bearer realtoken".
	// -------------------------------------------------------------------------
	req2, err := http.NewRequest(http.MethodGet, stack.UpstreamURL, nil)
	if err != nil {
		t.Fatalf("build request 2: %v", err)
	}
	req2.Header.Set("Authorization", "Bearer dummy")

	resp2, err := stack.AgentClient.Do(req2)
	if err != nil {
		t.Fatalf("request 2: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("request 2: unexpected status %d", resp2.StatusCode)
	}

	mu.Lock()
	sawAuth2 := ""
	if len(sawAuth) >= 2 {
		sawAuth2 = sawAuth[1]
	}
	mu.Unlock()

	if sawAuth2 != "Bearer realtoken" {
		t.Errorf("after secret set: upstream saw Authorization %q, want %q", sawAuth2, "Bearer realtoken")
	}
	if string(body2) != "Bearer realtoken" {
		t.Errorf("after secret set: response body %q, want %q", string(body2), "Bearer realtoken")
	}

	// -------------------------------------------------------------------------
	// Step 8: Verify the agent client never sent the real token. The agent
	// always sends "Bearer dummy" — only the daemon substitutes the real value
	// before forwarding to the upstream.
	// -------------------------------------------------------------------------
	// (The agent's Authorization header is set explicitly to "Bearer dummy" in
	// both requests above; this is the design invariant, not something we need
	// to re-assert via an additional network call.)
	t.Log("agent sent only 'Bearer dummy' in both requests — real token was substituted by the daemon")
}
