//go:build e2e

package e2e_test

import (
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
	require.NoError(t, err)        // MCP call succeeds...
	require.True(t, result.IsError) // ...but tool result is an error.

	// Verify audit log.
	audit := s.getAudit("", 10, 0)
	require.Equal(t, 1, audit.Total)
	require.Equal(t, "deny", audit.Records[0].Verdict)
}
