package tools

import (
	"context"
	"strings"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

// textOf extracts the text from the first content item of a CallToolResult.
func textOf(res *gomcp.CallToolResult) string {
	if res == nil || len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].(gomcp.TextContent).Text
}

func TestGhWhoami(t *testing.T) {
	mock := &mockGHClient{
		viewUserFunc: func(_ context.Context) (string, error) {
			return `{"login":"octocat","name":"The Octocat","html_url":"https://github.com/octocat","type":"User"}`, nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_whoami", Arguments: map[string]any{}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := textOf(res)
	if !strings.Contains(out, "Logged in as `octocat`") {
		t.Errorf("missing login line, got: %s", out)
	}
	if !strings.Contains(out, "(The Octocat)") {
		t.Errorf("missing name, got: %s", out)
	}
	if !strings.Contains(out, "https://github.com/octocat") {
		t.Errorf("missing html_url, got: %s", out)
	}
}

func TestGhWhoamiBot(t *testing.T) {
	mock := &mockGHClient{
		viewUserFunc: func(_ context.Context) (string, error) {
			return `{"login":"dependabot","name":null,"html_url":"https://github.com/dependabot","type":"Bot"}`, nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_whoami", Arguments: map[string]any{}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := textOf(res)
	if !strings.Contains(out, "`dependabot` [bot]") {
		t.Errorf("missing [bot] suffix, got: %s", out)
	}
	if strings.Contains(out, "()") {
		t.Errorf("null name should be omitted entirely, got: %s", out)
	}
}
