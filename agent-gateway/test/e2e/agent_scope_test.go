//go:build e2e

package e2e_test

// TestAgentScopeFilter verifies that the rule `agents` filter selects MITM
// vs tunnel per authenticated agent.
//
// Scenario:
//  1. Start the stack with two registered agents: "a1" and "a2".
//  2. Write a rule scoped to agents=["a1"] matching host "localhost".
//  3. a1 CONNECT to localhost:PORT → proxy sees a rule for a1 → MITM.
//     Audit row: interception=mitm, matched_rule="scope-a1".
//  4. a2 CONNECT to localhost:PORT → proxy finds no rule for a2 → tunnel.
//     Audit row: interception=tunnel, matched_rule=NULL.

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestAgentScopeFilter(t *testing.T) {
	// ─────────────────────────────────────────────────────────────────────────
	// 1. Start the stack with a simple upstream.
	// ─────────────────────────────────────────────────────────────────────────
	stack := newTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))

	token := adminToken(t, stack)

	// ─────────────────────────────────────────────────────────────────────────
	// 2. Replace the catch-all rule (written by newTestStack) with a rule that
	//    is scoped to agent "a1" only. After the reload:
	//      HostsForAgent("a1") = {"localhost"}   → MITM
	//      HostsForAgent("a2") = {}               → tunnel (no matching rule)
	// ─────────────────────────────────────────────────────────────────────────

	// Remove the global catch-all so only the scoped rule exists.
	catchAllPath := stack.RulesDir + "/zz-allow-all.hcl"
	if err := os.Remove(catchAllPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove catch-all rule: %v", err)
	}

	stack.writeRule(t, "scope-a1.hcl", `
rule "scope-a1" {
  agents  = ["a1"]
  verdict = "allow"
  match {
    host = "localhost"
  }
}
`)

	// ─────────────────────────────────────────────────────────────────────────
	// 3. Register agents a1 and a2, then reload rules so the scope rule and the
	//    updated registry are both live.
	// ─────────────────────────────────────────────────────────────────────────
	tokenA1 := stack.agentAdd(t, "a1")
	tokenA2 := stack.agentAdd(t, "a2")
	stack.rulesReload(t)
	// Allow time for SIGHUP-triggered rules + registry reload to settle.
	time.Sleep(300 * time.Millisecond)

	// Parse the upstream port from stack.UpstreamURL so we can build the
	// CONNECT target as "localhost:PORT" explicitly.
	upURL, err := url.Parse(stack.UpstreamURL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	upstreamHost := upURL.Host // "localhost:PORT"

	// ─────────────────────────────────────────────────────────────────────────
	// 4. Build per-agent HTTP clients.
	//    - a1Client trusts only the gateway CA (MITM certs are from the gateway).
	//    - a2Client uses InsecureSkipVerify so the tunnelled upstream cert is
	//      accepted regardless of CA, letting us observe the audit row.
	// ─────────────────────────────────────────────────────────────────────────
	a1Client := stack.agentHTTPClient(t, stack.CAPEM, tokenA1)

	proxyURL, _ := url.Parse("http://x:" + tokenA2 + "@" + stack.ProxyAddr)
	a2Client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec — tunnel test only
			},
		},
	}

	targetURL := "https://" + upstreamHost

	// ─────────────────────────────────────────────────────────────────────────
	// 5. Fire requests as a1 and a2.
	// ─────────────────────────────────────────────────────────────────────────

	// a1 — expects MITM (gateway CA cert, rule matches).
	respA1, err := a1Client.Get(targetURL)
	if err != nil {
		t.Fatalf("a1 GET: %v", err)
	}
	respA1.Body.Close()
	if respA1.StatusCode != http.StatusOK {
		t.Fatalf("a1: unexpected status %d", respA1.StatusCode)
	}

	// a2 — expects tunnel. The upstream presents its own cert (signed by
	// upstreamCA, not gateway CA). With InsecureSkipVerify the request succeeds,
	// but the audit records interception=tunnel.
	respA2, err := a2Client.Get(targetURL)
	if err != nil {
		// A tunnel request with InsecureSkipVerify should succeed. If it errors,
		// it likely means the dial failed (port unavailable), not a scope failure.
		t.Fatalf("a2 GET: %v", err)
	}
	respA2.Body.Close()

	// Allow the audit entries to be written to the DB before we query.
	time.Sleep(200 * time.Millisecond)

	// ─────────────────────────────────────────────────────────────────────────
	// 6. Query the audit log and verify the interception modes.
	// ─────────────────────────────────────────────────────────────────────────
	a1Row := latestAuditRow(t, stack, token, "a1")
	a2Row := latestAuditRow(t, stack, token, "a2")

	// a1 must have been MITM'd with the scoped rule.
	if got := a1Row.Interception; got != "mitm" {
		t.Errorf("a1: interception=%q, want %q", got, "mitm")
	}
	if a1Row.MatchedRule == nil || *a1Row.MatchedRule != "scope-a1" {
		got := "<nil>"
		if a1Row.MatchedRule != nil {
			got = *a1Row.MatchedRule
		}
		t.Errorf("a1: matched_rule=%q, want %q", got, "scope-a1")
	}

	// a2 must have been tunnelled (no rule applies to a2).
	if got := a2Row.Interception; got != "tunnel" {
		t.Errorf("a2: interception=%q, want %q", got, "tunnel")
	}
	if a2Row.MatchedRule != nil {
		t.Errorf("a2: matched_rule=%q, want nil (tunnel rows have no matched rule)", *a2Row.MatchedRule)
	}
}

// auditRow is a minimal projection of audit.Entry for assertion purposes.
type auditRow struct {
	Interception string
	MatchedRule  *string
}

// latestAuditRow fetches the most recent audit entry for the given agent via
// the dashboard API and returns a minimal projection. The test fails if no
// matching row is found within a brief polling window.
func latestAuditRow(t *testing.T, stack *TestStack, adminTok, agentName string) auditRow {
	t.Helper()

	var row auditRow
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
				Interception string  `json:"Interception"`
				MatchedRule  *string `json:"MatchedRule"`
			} `json:"records"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode audit: %v", err)
		}
		if payload.Total > 0 && len(payload.Records) > 0 {
			r := payload.Records[0]
			row.Interception = r.Interception
			row.MatchedRule = r.MatchedRule
			return row
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no audit row found for agent %q within 5s", agentName)
	return row
}
