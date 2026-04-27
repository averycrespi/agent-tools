package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func TestGhListBranches(t *testing.T) {
	mock := &mockGHClient{
		listBranchesFunc: func(_ context.Context, _, _ string, _, _ int) (string, error) {
			return `[
				{"name":"main","commit":{"sha":"abc1234xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}},
				{"name":"feat/foo","commit":{"sha":"def5678xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}}
			]`, nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_list_branches", Arguments: map[string]any{
			"owner": "x", "repo": "y",
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := textOf(res)
	for _, want := range []string{"`main`", "(abc1234)", "`feat/foo`", "(def5678)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestGhListBranchesTruncates(t *testing.T) {
	parts := make([]string, 40)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"name":"b%d","commit":{"sha":"0000000xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}}`, i)
	}
	payload := "[" + strings.Join(parts, ",") + "]"
	mock := &mockGHClient{
		listBranchesFunc: func(_ context.Context, _, _ string, _, _ int) (string, error) { return payload, nil },
	}
	h := NewHandler(mock)
	res, _ := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_list_branches", Arguments: map[string]any{
			"owner": "x", "repo": "y",
		}},
	})
	if !strings.Contains(textOf(res), "showing 30 of 40") {
		t.Errorf("expected truncation trailer")
	}
}

func TestGhListBranchesPagePassed(t *testing.T) {
	var gotPage int
	mock := &mockGHClient{
		listBranchesFunc: func(_ context.Context, _, _ string, _, page int) (string, error) {
			gotPage = page
			return `[]`, nil
		},
	}
	h := NewHandler(mock)
	_, _ = h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_list_branches", Arguments: map[string]any{
			"owner": "x", "repo": "y", "page": float64(3),
		}},
	})
	if gotPage != 3 {
		t.Errorf("expected page=3 to be passed through, got %d", gotPage)
	}
}

func TestGhListBranchesPageDefaultsToOne(t *testing.T) {
	var gotPage int
	mock := &mockGHClient{
		listBranchesFunc: func(_ context.Context, _, _ string, _, page int) (string, error) {
			gotPage = page
			return `[]`, nil
		},
	}
	h := NewHandler(mock)
	_, _ = h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_list_branches", Arguments: map[string]any{
			"owner": "x", "repo": "y",
		}},
	})
	if gotPage != 1 {
		t.Errorf("expected page to default to 1, got %d", gotPage)
	}
}

func TestListBranches_Empty(t *testing.T) {
	h := NewHandler(&mockGHClient{
		listBranchesFunc: func(_ context.Context, _, _ string, _, _ int) (string, error) {
			return `[]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_branches"
	req.Params.Arguments = map[string]any{"owner": "octocat", "repo": "hello-world"}
	result, err := h.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatal("expected non-error result")
	}
	text, ok := result.Content[0].(gomcp.TextContent)
	if !ok {
		t.Fatal("expected TextContent")
	}
	if text.Text != "No branches found." {
		t.Errorf("got %q, want %q", text.Text, "No branches found.")
	}
}
