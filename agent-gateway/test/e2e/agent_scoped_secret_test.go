//go:build e2e

package e2e_test

// TestAgentScopedSecretResolution verifies that agent-scoped secrets resolve
// by the authenticated agent's name (not the matched rule's name).
//
// Scenario:
//  1. Register agent "a1" and build a client authenticated as a1.
//  2. Set a global secret: gh_bot = "global-token".
//  3. Set an agent-scoped secret for a1: gh_bot = "agent-token".
//     (The rule name is "inject-gh-bot" — deliberately different from the
//     agent name "a1" so that a pipeline that mistakenly passes the rule name
//     in place of the agent name falls back to the global row.)
//  4. Write a rule matching the upstream host that injects
//     Authorization: Bearer ${secrets.gh_bot}.
//  5. a1 sends a request. Upstream must see "Bearer agent-token" (the
//     agent-scoped value wins over the global one).
//  6. The audit row for a1 must show credential_scope=agent:a1.
//
// Regression guard: before the fix at pipeline.go:336, the injector received
// the matched rule name instead of the agent name, so the query
//     scope IN ('global', 'agent:' || ?2)
// resolved to 'global' (no row named "agent:inject-gh-bot") and upstream saw
// "Bearer global-token" with credential_scope=global.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"
)

func TestAgentScopedSecretResolution(t *testing.T) {
	// Step 1: stack + echo-the-Authorization-header upstream.
	var (
		mu      sync.Mutex
		sawAuth []string
	)
	stack := newTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		sawAuth = append(sawAuth, auth)
		mu.Unlock()
		fmt.Fprint(w, auth)
	}))

	// Register agent "a1" and build a client for it.
	tokenA1 := stack.agentAdd(t, "a1")
	a1Client := stack.agentHTTPClient(t, stack.CAPEM, tokenA1)

	upstreamURL, err := url.Parse(stack.UpstreamURL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	upstreamHost := upstreamURL.Hostname()

	// Step 2 + 3: write both a global and an a1-scoped value for the same name,
	// with different values, so we can tell which scope the injector resolved.
	stack.setSecret(t, "gh_bot", "global-token")
	stack.setAgentSecret(t, "a1", "gh_bot", "agent-token")

	// Step 4: replace the catch-all rule with one whose name differs from the
	// agent name. If the pipeline passes the rule name instead of the agent
	// name, the query will look for 'agent:inject-gh-bot' (which does not exist)
	// and silently fall back to 'global', defeating the test.
	catchAllPath := stack.RulesDir + "/zz-allow-all.hcl"
	if err := os.Remove(catchAllPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove catch-all rule: %v", err)
	}
	stack.writeRule(t, "inject-gh-bot.hcl", fmt.Sprintf(`
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
`, upstreamHost))

	stack.rulesReload(t)
	// Allow rules + registry reload and cache invalidation to settle.
	time.Sleep(300 * time.Millisecond)

	// Step 5: a1 sends a request. Upstream must see the agent-scoped token.
	req, err := http.NewRequest(http.MethodGet, stack.UpstreamURL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer dummy")

	resp, err := a1Client.Do(req)
	if err != nil {
		t.Fatalf("a1 GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d", resp.StatusCode)
	}

	mu.Lock()
	got := ""
	if len(sawAuth) > 0 {
		got = sawAuth[0]
	}
	mu.Unlock()

	if got != "Bearer agent-token" {
		t.Errorf("upstream Authorization = %q, want %q (agent-scoped secret must win over global)", got, "Bearer agent-token")
	}
	if string(body) != "Bearer agent-token" {
		t.Errorf("response body = %q, want %q", string(body), "Bearer agent-token")
	}

	// Step 6: audit row for a1 must show credential_scope=agent:a1.
	time.Sleep(200 * time.Millisecond)
	row := latestCredScopeRow(t, stack, adminToken(t, stack), "a1")
	if row.CredentialScope == nil || *row.CredentialScope != "agent:a1" {
		gotScope := "<nil>"
		if row.CredentialScope != nil {
			gotScope = *row.CredentialScope
		}
		t.Errorf("audit credential_scope = %q, want %q", gotScope, "agent:a1")
	}
	if row.Injection == nil || *row.Injection != "applied" {
		gotInj := "<nil>"
		if row.Injection != nil {
			gotInj = *row.Injection
		}
		t.Errorf("audit injection = %q, want %q", gotInj, "applied")
	}
}

// credScopeRow is a minimal projection of audit.Entry for credential-scope
// assertions.
type credScopeRow struct {
	Injection       *string
	CredentialScope *string
}

// latestCredScopeRow fetches the most recent audit entry for the given agent
// via the dashboard API and projects it onto credScopeRow. The test fails if
// no matching row is found within a brief polling window.
func latestCredScopeRow(t *testing.T, stack *TestStack, adminTok, agentName string) credScopeRow {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp := dashGet(t, stack, adminTok,
			fmt.Sprintf("/dashboard/api/audit?agent=%s&limit=1", agentName))
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("read audit response: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("audit API: status %d: %s", resp.StatusCode, body)
		}

		var payload struct {
			Records []struct {
				Injection       *string `json:"Injection"`
				CredentialScope *string `json:"CredentialScope"`
			} `json:"records"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode audit: %v", err)
		}
		if payload.Total > 0 && len(payload.Records) > 0 {
			r := payload.Records[0]
			return credScopeRow{Injection: r.Injection, CredentialScope: r.CredentialScope}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no audit row found for agent %q within 5s", agentName)
	return credScopeRow{}
}
