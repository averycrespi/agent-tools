//go:build e2e

package e2e_test

import (
	"encoding/json"
	"testing"
	"time"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

var defaultTools = []toolDef{
	{Name: "say_hello", Description: "Says hello", Response: `{"message":"hello"}`},
}

func TestE2E_ApproveToolCall(t *testing.T) {
	s := newTestStack(t, stackOpts{Tools: defaultTools})

	// Call tool in goroutine — it blocks on approval.
	type callResult struct {
		text string
		err  error
	}
	ch := make(chan callResult, 1)
	go func() {
		result, err := s.callTool("echo.say_hello", map[string]any{})
		if err != nil {
			ch <- callResult{err: err}
			return
		}
		// Extract text from first content block.
		text := ""
		if len(result.Content) > 0 {
			if tc, ok := result.Content[0].(gomcp.TextContent); ok {
				text = tc.Text
			}
		}
		ch <- callResult{text: text}
	}()

	// Wait for pending request, then approve.
	pending := s.waitForPending(5 * time.Second)
	require.Len(t, pending, 1)
	require.Equal(t, "echo.say_hello", pending[0].Tool)
	s.approve(pending[0].ID)

	// Wait for tool call to complete.
	r := <-ch
	require.NoError(t, r.err)
	require.Contains(t, r.text, "hello")

	// Verify audit log.
	audit := s.getAudit("", 10, 0)
	require.Equal(t, 1, audit.Total)
	require.Equal(t, "echo.say_hello", audit.Records[0].Tool)
	require.Equal(t, "require-approval", audit.Records[0].Verdict)
	require.NotNil(t, audit.Records[0].Approved)
	require.True(t, *audit.Records[0].Approved)
}

func TestE2E_DenyToolCall(t *testing.T) {
	s := newTestStack(t, stackOpts{Tools: defaultTools})

	type callResult struct {
		isError bool
		err     error
	}
	ch := make(chan callResult, 1)
	go func() {
		result, err := s.callTool("echo.say_hello", map[string]any{})
		if err != nil {
			ch <- callResult{err: err}
			return
		}
		ch <- callResult{isError: result.IsError}
	}()

	pending := s.waitForPending(5 * time.Second)
	require.Len(t, pending, 1)
	s.deny(pending[0].ID)

	r := <-ch
	require.NoError(t, r.err)  // MCP call itself succeeds...
	require.True(t, r.isError) // ...but the tool result is an error.

	// Verify audit log.
	audit := s.getAudit("", 10, 0)
	require.Equal(t, 1, audit.Total)
	require.NotNil(t, audit.Records[0].Approved)
	require.False(t, *audit.Records[0].Approved)
}

func TestE2E_AllowedToolCall(t *testing.T) {
	s := newTestStack(t, stackOpts{
		Tools: defaultTools,
		Rules: []testRuleConfig{{Tool: "echo.*", Verdict: "allow"}},
	})

	// Tool call should return immediately (no approval needed).
	result, err := s.callTool("echo.say_hello", map[string]any{})
	require.NoError(t, err)
	require.False(t, result.IsError)

	// Verify audit log shows verdict=allow and no approval field.
	audit := s.getAudit("", 10, 0)
	require.Equal(t, 1, audit.Total)
	require.Equal(t, "allow", audit.Records[0].Verdict)
	require.Nil(t, audit.Records[0].Approved)
}

func TestE2E_DeniedByRules(t *testing.T) {
	s := newTestStack(t, stackOpts{
		Tools: defaultTools,
		Rules: []testRuleConfig{{Tool: "echo.*", Verdict: "deny"}},
	})

	// Tool call should return an error immediately.
	result, err := s.callTool("echo.say_hello", map[string]any{})
	require.NoError(t, err)         // MCP call succeeds...
	require.True(t, result.IsError) // ...but tool result is an error.

	// Verify audit log.
	audit := s.getAudit("", 10, 0)
	require.Equal(t, 1, audit.Total)
	require.Equal(t, "deny", audit.Records[0].Verdict)
}

func TestE2E_DashboardToolsListing(t *testing.T) {
	tools := []toolDef{
		{Name: "greet", Description: "Greets the user", Response: `"hi"`},
		{Name: "farewell", Description: "Says goodbye", Response: `"bye"`},
		{Name: "status", Description: "Returns status", Response: `"ok"`},
	}
	s := newTestStack(t, stackOpts{
		Tools: tools,
		Rules: []testRuleConfig{{Tool: "*", Verdict: "allow"}},
	})

	resp := s.getTools()
	require.Len(t, resp.Tools, 3)

	// Tools should be sorted by name and prefixed with server name.
	names := make([]string, len(resp.Tools))
	for i, tool := range resp.Tools {
		names[i] = tool.Name
	}
	require.Contains(t, names, "echo.farewell")
	require.Contains(t, names, "echo.greet")
	require.Contains(t, names, "echo.status")

	// Verify descriptions are preserved.
	for _, tool := range resp.Tools {
		if tool.Name == "echo.greet" {
			require.Equal(t, "Greets the user", tool.Description)
		}
	}
}

func TestE2E_ArgMatchingGate(t *testing.T) {
	s := newTestStack(t, stackOpts{
		Tools: defaultTools,
		Rules: []testRuleConfig{
			{
				Tool:    "echo.say_hello",
				Verdict: "allow",
				Args: []testArgPattern{
					{Path: "name", Match: json.RawMessage(`"alice"`)},
				},
			},
			{Tool: "*", Verdict: "deny"},
		},
	})

	// Matching args → allow rule fires, backend returns the response.
	result, err := s.callTool("echo.say_hello", map[string]any{"name": "alice"})
	require.NoError(t, err)
	require.False(t, result.IsError)

	// Non-matching args → falls through to deny rule.
	result, err = s.callTool("echo.say_hello", map[string]any{"name": "bob"})
	require.NoError(t, err)
	require.True(t, result.IsError)

	// Audit log captures both verdicts.
	audit := s.getAudit("", 10, 0)
	require.Equal(t, 2, audit.Total)
	verdicts := []string{audit.Records[0].Verdict, audit.Records[1].Verdict}
	require.Contains(t, verdicts, "allow")
	require.Contains(t, verdicts, "deny")
}

func TestE2E_AuditLogPagination(t *testing.T) {
	tools := []toolDef{
		{Name: "say_hello", Description: "Says hello", Response: `{"message":"hello"}`},
		{Name: "say_bye", Description: "Says bye", Response: `{"message":"bye"}`},
	}
	s := newTestStack(t, stackOpts{
		Tools: tools,
		Rules: []testRuleConfig{{Tool: "*", Verdict: "allow"}},
	})

	// Make 5 tool calls (3 say_hello, 2 say_bye).
	for i := 0; i < 3; i++ {
		_, err := s.callTool("echo.say_hello", map[string]any{})
		require.NoError(t, err)
	}
	for i := 0; i < 2; i++ {
		_, err := s.callTool("echo.say_bye", map[string]any{})
		require.NoError(t, err)
	}

	// Verify total count.
	all := s.getAudit("", 50, 0)
	require.Equal(t, 5, all.Total)

	// Verify pagination: page 1.
	page1 := s.getAudit("", 2, 0)
	require.Len(t, page1.Records, 2)
	require.Equal(t, 5, page1.Total)

	// Verify pagination: page 2.
	page2 := s.getAudit("", 2, 2)
	require.Len(t, page2.Records, 2)

	// Verify pagination: page 3 (partial).
	page3 := s.getAudit("", 2, 4)
	require.Len(t, page3.Records, 1)

	// Verify filtering by tool name.
	filtered := s.getAudit("say_hello", 50, 0)
	require.Equal(t, 3, filtered.Total)
	for _, rec := range filtered.Records {
		require.Contains(t, rec.Tool, "say_hello")
	}
}
