//go:build e2e

package e2e_test

// TestBodyMatcher_JSONBodyMatches verifies end-to-end that json_body matchers
// fire on buffered request bodies.
//
// Scenario:
//  1. Start the stack with a mock upstream that echoes back a sentinel
//     response header set by the inject block.
//  2. Write two rules:
//     - "body-deny": deny when json_body $.action matches "^forbidden$".
//     - "body-allow": allow (with inject) when json_body $.action matches "^send$".
//  3. Reload rules via SIGHUP.
//  4. Agent POSTs JSON body {"action":"send"} — must match "body-allow".
//     Upstream must observe X-Body-Matched: true (injected by the rule).
//  5. Agent POSTs JSON body {"action":"forbidden"} — must be denied (403).
//  6. Agent POSTs JSON body {"action":"other"} — neither body rule matches;
//     the catch-all allow-all rule fires, no injection, upstream sees request
//     without X-Body-Matched.

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBodyMatcher_JSONBodyMatches(t *testing.T) {
	// -------------------------------------------------------------------------
	// Step 1: spin up the stack. The mock upstream echoes back the value of
	// X-Body-Matched so we can assert that injection fired.
	// -------------------------------------------------------------------------
	stack := newTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		matched := r.Header.Get("X-Body-Matched")
		w.Header().Set("X-Body-Matched", matched)
		fmt.Fprintf(w, "upstream received: %s", matched)
	}))

	// Extract hostname (no port) for the rule match.
	upstreamURL, err := url.Parse(stack.UpstreamURL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	upstreamHost := upstreamURL.Hostname()

	// -------------------------------------------------------------------------
	// Step 2: write rules with json_body matchers.
	// The inject block uses a plain literal value (no secret reference needed).
	// -------------------------------------------------------------------------
	ruleContent := fmt.Sprintf(`
rule "body-deny" {
  verdict = "deny"
  match {
    host   = %q
    method = "POST"
    json_body {
      jsonpath "$.action" {
        matches = "^forbidden$"
      }
    }
  }
}

rule "body-allow" {
  verdict = "allow"
  match {
    host   = %q
    method = "POST"
    json_body {
      jsonpath "$.action" {
        matches = "^send$"
      }
    }
  }
  inject {
    replace_header = {
      "X-Body-Matched" = "true"
    }
  }
}
`, upstreamHost, upstreamHost)
	// Write before the catch-all (alphabetical: "aa-body.hcl" < "zz-allow-all.hcl").
	stack.writeRule(t, "aa-body.hcl", ruleContent)

	// -------------------------------------------------------------------------
	// Step 3: trigger rules reload and wait for the daemon to apply it.
	// -------------------------------------------------------------------------
	stack.rulesReload(t)
	time.Sleep(300 * time.Millisecond)

	// -------------------------------------------------------------------------
	// Step 4: POST {"action":"send"} — must match "body-allow"; upstream must
	// observe X-Body-Matched: true (injected by the matching rule).
	// -------------------------------------------------------------------------
	req4, err := http.NewRequest(http.MethodPost, stack.UpstreamURL,
		strings.NewReader(`{"action":"send"}`))
	if err != nil {
		t.Fatalf("build send request: %v", err)
	}
	req4.Header.Set("Content-Type", "application/json")

	resp4, err := stack.AgentClient.Do(req4)
	if err != nil {
		t.Fatalf("send POST: %v", err)
	}
	_, _ = io.ReadAll(resp4.Body)
	resp4.Body.Close()

	if resp4.StatusCode != http.StatusOK {
		t.Fatalf("send POST: unexpected status %d (want 200)", resp4.StatusCode)
	}
	if got := resp4.Header.Get("X-Body-Matched"); got != "true" {
		t.Errorf("send POST: upstream X-Body-Matched = %q, want %q (body matcher did not fire)", got, "true")
	}

	// -------------------------------------------------------------------------
	// Step 5: POST {"action":"forbidden"} — must be denied (403).
	// -------------------------------------------------------------------------
	req5, err := http.NewRequest(http.MethodPost, stack.UpstreamURL,
		strings.NewReader(`{"action":"forbidden"}`))
	if err != nil {
		t.Fatalf("build forbidden request: %v", err)
	}
	req5.Header.Set("Content-Type", "application/json")

	resp5, err := stack.AgentClient.Do(req5)
	if err != nil {
		t.Fatalf("forbidden POST: %v", err)
	}
	_, _ = io.ReadAll(resp5.Body)
	resp5.Body.Close()

	if resp5.StatusCode != http.StatusForbidden {
		t.Errorf("forbidden POST: unexpected status %d (want 403)", resp5.StatusCode)
	}

	// -------------------------------------------------------------------------
	// Step 6: POST {"action":"other"} — neither body rule matches; catch-all
	// fires, no injection, upstream sees no X-Body-Matched header.
	// -------------------------------------------------------------------------
	req6, err := http.NewRequest(http.MethodPost, stack.UpstreamURL,
		strings.NewReader(`{"action":"other"}`))
	if err != nil {
		t.Fatalf("build other request: %v", err)
	}
	req6.Header.Set("Content-Type", "application/json")

	resp6, err := stack.AgentClient.Do(req6)
	if err != nil {
		t.Fatalf("other POST: %v", err)
	}
	_, _ = io.ReadAll(resp6.Body)
	resp6.Body.Close()

	if resp6.StatusCode != http.StatusOK {
		t.Errorf("other POST: unexpected status %d (want 200)", resp6.StatusCode)
	}
	if got := resp6.Header.Get("X-Body-Matched"); got != "" {
		t.Errorf("other POST: unexpected X-Body-Matched = %q (no body rule should have matched)", got)
	}
}
