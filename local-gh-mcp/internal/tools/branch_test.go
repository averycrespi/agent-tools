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
		listBranchesFunc: func(_ context.Context, _, _ string, _ int) (string, error) {
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
		listBranchesFunc: func(_ context.Context, _, _ string, _ int) (string, error) { return payload, nil },
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
