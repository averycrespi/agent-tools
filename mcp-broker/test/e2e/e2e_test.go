//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
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
		text    string
		err     error
	}
	ch := make(chan callResult, 1)
	go func() {
		result, err := s.callTool("echo.say_hello", map[string]any{})
		if err != nil {
			ch <- callResult{err: err}
			return
		}
		text := ""
		if len(result.Content) > 0 {
			if tc, ok := result.Content[0].(gomcp.TextContent); ok {
				text = tc.Text
			}
		}
		ch <- callResult{isError: result.IsError, text: text}
	}()

	pending := s.waitForPending(5 * time.Second)
	require.Len(t, pending, 1)
	s.denyWithReason(pending[0].ID, "needs narrower scope")

	r := <-ch
	require.NoError(t, r.err)  // MCP call itself succeeds...
	require.True(t, r.isError) // ...but the tool result is an error.
	require.Contains(t, r.text, "denied by user: needs narrower scope")

	// Verify audit log.
	audit := s.getAudit("", 10, 0)
	require.Equal(t, 1, audit.Total)
	require.NotNil(t, audit.Records[0].Approved)
	require.False(t, *audit.Records[0].Approved)
}

func TestE2E_RejectApprovalModeDoesNotQueueApproval(t *testing.T) {
	s := newTestStack(t, stackOpts{Tools: defaultTools})

	result, err := s.callToolWithHeaders("echo.say_hello", map[string]any{}, http.Header{
		"Mcp-Broker-Approval-Mode": []string{"reject"},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.NotEmpty(t, result.Content)
	if tc, ok := result.Content[0].(gomcp.TextContent); ok {
		require.Contains(t, tc.Text, "tool call blocked: approval is required for echo.say_hello")
		require.Contains(t, tc.Text, "Mcp-Broker-Approval-Mode=reject")
	} else {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}

	require.Empty(t, s.getPending())

	audit := s.getAudit("", 10, 0)
	require.Equal(t, 1, audit.Total)
	require.Equal(t, "require-approval", audit.Records[0].Verdict)
	require.NotNil(t, audit.Records[0].Approved)
	require.False(t, *audit.Records[0].Approved)
	require.Equal(t, "approval-mode: reject", audit.Records[0].DenialReason)
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

func TestE2E_UpstreamErrorResultReachesClient(t *testing.T) {
	s := newTestStack(t, stackOpts{
		Tools: []toolDef{{Name: "fails", Response: "upstream rejected request", ResultIsError: true}},
		Rules: []testRuleConfig{{Tool: "echo.*", Verdict: "allow"}},
	})

	result, err := s.callTool("echo.fails", map[string]any{})
	require.NoError(t, err)
	require.True(t, result.IsError)
}

func TestE2E_BackendCallFailureReachesClient(t *testing.T) {
	s := newTestStack(t, stackOpts{
		Tools: []toolDef{{Name: "fails", CallFailure: true}},
		Rules: []testRuleConfig{{Tool: "echo.*", Verdict: "allow"}},
	})

	result, err := s.callTool("echo.fails", map[string]any{})
	require.NoError(t, err)
	require.True(t, result.IsError)
}

func TestE2E_DeniedByRules(t *testing.T) {
	s := newTestStack(t, stackOpts{
		Tools: defaultTools,
		Rules: []testRuleConfig{{Tool: "echo.*", Verdict: "deny", Reason: "read-only session"}},
	})

	// Tool call should return an error immediately.
	result, err := s.callTool("echo.say_hello", map[string]any{})
	require.NoError(t, err)         // MCP call succeeds...
	require.True(t, result.IsError) // ...but tool result is an error.
	require.NotEmpty(t, result.Content)
	if tc, ok := result.Content[0].(gomcp.TextContent); ok {
		require.Contains(t, tc.Text, "denied by rule: read-only session")
	} else {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}

	// Verify audit log.
	audit := s.getAudit("", 10, 0)
	require.Equal(t, 1, audit.Total)
	require.Equal(t, "deny", audit.Records[0].Verdict)
}

func TestE2E_AnnotationsRoundTripThroughBroker(t *testing.T) {
	readOnly := true
	destructive := false
	s := newTestStack(t, stackOpts{
		Tools: []toolDef{
			{
				Name:        "search",
				Description: "Search the index",
				Response:    `{"hits":0}`,
				Annotations: &gomcp.ToolAnnotation{
					Title:           "Search",
					ReadOnlyHint:    &readOnly,
					DestructiveHint: &destructive,
				},
				OutputSchema: &gomcp.ToolOutputSchema{
					Type:       "object",
					Properties: map[string]any{"hits": map[string]any{"type": "integer"}},
				},
			},
			{Name: "plain", Description: "No extras", Response: `"ok"`},
		},
		Rules: []testRuleConfig{{Tool: "*", Verdict: "allow"}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := s.Client.ListTools(ctx, gomcp.ListToolsRequest{})
	require.NoError(t, err)

	byName := make(map[string]gomcp.Tool, len(resp.Tools))
	for _, tool := range resp.Tools {
		byName[tool.Name] = tool
	}

	rich := byName["echo.search"]
	require.Equal(t, "Search", rich.Annotations.Title)
	require.NotNil(t, rich.Annotations.ReadOnlyHint)
	require.True(t, *rich.Annotations.ReadOnlyHint)
	require.NotNil(t, rich.Annotations.DestructiveHint)
	require.False(t, *rich.Annotations.DestructiveHint)
	require.Equal(t, "object", rich.OutputSchema.Type)
	require.Contains(t, rich.OutputSchema.Properties, "hits")

	plain := byName["echo.plain"]
	require.Empty(t, plain.Annotations.Title)
	require.Nil(t, plain.Annotations.ReadOnlyHint)
	require.Empty(t, plain.OutputSchema.Type)
}

func TestE2E_StructuredContentRoundTripThroughBroker(t *testing.T) {
	s := newTestStack(t, stackOpts{
		Tools: []toolDef{
			{
				Name:               "search",
				Description:        "Search the index",
				Response:           `{"hits":1}`,
				StructuredResponse: map[string]any{"hits": 1},
				OutputSchema: &gomcp.ToolOutputSchema{
					Type:       "object",
					Properties: map[string]any{"hits": map[string]any{"type": "integer"}},
				},
			},
		},
		Rules: []testRuleConfig{{Tool: "*", Verdict: "allow"}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	list, err := s.Client.ListTools(ctx, gomcp.ListToolsRequest{})
	require.NoError(t, err)
	require.Len(t, list.Tools, 1)
	require.Equal(t, "object", list.Tools[0].OutputSchema.Type)

	result, err := s.callTool("echo.search", map[string]any{})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.NotNil(t, result.StructuredContent)
	require.Equal(t, map[string]any{"hits": float64(1)}, result.StructuredContent)

	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(gomcp.TextContent)
	require.True(t, ok)
	require.Equal(t, `{"hits":1}`, text.Text)
}

func TestE2E_ToolPatchesDisabledTool(t *testing.T) {
	s := newTestStack(t, stackOpts{
		Tools: []toolDef{
			{Name: "search", Description: "Search", Response: `{"hits":0}`},
			{Name: "delete", Description: "Delete", Response: `{"deleted":true}`},
		},
		Rules: []testRuleConfig{{Tool: "*", Verdict: "allow"}},
		ToolPatches: []testToolPatchConfig{
			{Tool: "echo.delete", Disabled: true},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	list, err := s.Client.ListTools(ctx, gomcp.ListToolsRequest{})
	require.NoError(t, err)
	names := make([]string, 0, len(list.Tools))
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	require.Contains(t, names, "echo.search")
	require.NotContains(t, names, "echo.delete")

	dashboard := s.getTools()
	dashboardNames := make([]string, 0, len(dashboard.Tools))
	for _, tool := range dashboard.Tools {
		dashboardNames = append(dashboardNames, tool.Name)
	}
	require.Contains(t, dashboardNames, "echo.search")
	require.NotContains(t, dashboardNames, "echo.delete")

	_, err = s.callTool("echo.delete", map[string]any{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool not found")
}

func TestE2E_ToolPatchesMergeAnnotations(t *testing.T) {
	backendReadOnly := false
	backendDestructive := true
	backendOpenWorld := true
	patchedTitle := "Patched search"
	patchedReadOnly := true
	patchedDestructive := false
	s := newTestStack(t, stackOpts{
		Tools: []toolDef{
			{
				Name:        "search",
				Description: "Search",
				Response:    `{"hits":0}`,
				Annotations: &gomcp.ToolAnnotation{
					Title:           "Backend search",
					ReadOnlyHint:    &backendReadOnly,
					DestructiveHint: &backendDestructive,
					OpenWorldHint:   &backendOpenWorld,
				},
			},
			{Name: "plain", Description: "Plain", Response: `"ok"`},
		},
		Rules: []testRuleConfig{{Tool: "*", Verdict: "allow"}},
		ToolPatches: []testToolPatchConfig{
			{
				Tool: "echo.search",
				Annotations: &testToolAnnotationsPatch{
					Title:           &patchedTitle,
					ReadOnlyHint:    &patchedReadOnly,
					DestructiveHint: &patchedDestructive,
				},
			},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := s.Client.ListTools(ctx, gomcp.ListToolsRequest{})
	require.NoError(t, err)

	byName := make(map[string]gomcp.Tool, len(resp.Tools))
	for _, tool := range resp.Tools {
		byName[tool.Name] = tool
	}
	patched := byName["echo.search"]
	require.Equal(t, patchedTitle, patched.Annotations.Title)
	require.NotNil(t, patched.Annotations.ReadOnlyHint)
	require.True(t, *patched.Annotations.ReadOnlyHint)
	require.NotNil(t, patched.Annotations.DestructiveHint)
	require.False(t, *patched.Annotations.DestructiveHint)
	require.NotNil(t, patched.Annotations.OpenWorldHint)
	require.True(t, *patched.Annotations.OpenWorldHint)
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
