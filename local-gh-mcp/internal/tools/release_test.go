package tools

import (
	"context"
	"strings"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func TestGhListReleases(t *testing.T) {
	mock := &mockGHClient{
		listReleasesFunc: func(_ context.Context, _, _ string, _ int) (string, error) {
			return `[
				{"tagName":"v1.2.3","name":"Feature X","publishedAt":"2026-04-15T10:00:00Z","isDraft":false,"isPrerelease":false},
				{"tagName":"v1.3.0-rc1","name":"","publishedAt":"2026-04-20T10:00:00Z","isDraft":false,"isPrerelease":true}
			]`, nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_list_releases", Arguments: map[string]any{
			"owner": "x", "repo": "y",
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := textOf(res)
	for _, want := range []string{"`v1.2.3`", "\"Feature X\"", "`v1.3.0-rc1`", "[prerelease]"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestGhViewReleaseLatest(t *testing.T) {
	var capturedTag string
	mock := &mockGHClient{
		viewReleaseFunc: func(_ context.Context, _, _ string, tag string) (string, error) {
			capturedTag = tag
			return `{"tagName":"v2.0.0","name":"Big Release","author":{"login":"octocat"},"publishedAt":"2026-04-22T10:00:00Z","body":"Release notes","assets":[{"name":"binary.tar.gz","size":1048576}]}`, nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_view_release", Arguments: map[string]any{
			"owner": "x", "repo": "y",
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedTag != "" {
		t.Errorf("omitted tag should pass empty string (latest), got: %q", capturedTag)
	}
	out := textOf(res)
	for _, want := range []string{"v2.0.0", "Big Release", "@octocat", "binary.tar.gz", "1.0 MiB"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestGhViewReleaseWithTag(t *testing.T) {
	var capturedTag string
	mock := &mockGHClient{
		viewReleaseFunc: func(_ context.Context, _, _ string, tag string) (string, error) {
			capturedTag = tag
			return `{"tagName":"v1.0.0","name":"","body":"","assets":[]}`, nil
		},
	}
	h := NewHandler(mock)
	_, _ = h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_view_release", Arguments: map[string]any{
			"owner": "x", "repo": "y", "tag": "v1.0.0",
		}},
	})
	if capturedTag != "v1.0.0" {
		t.Errorf("tag not threaded: %q", capturedTag)
	}
}

func TestListReleases_Empty(t *testing.T) {
	h := NewHandler(&mockGHClient{
		listReleasesFunc: func(_ context.Context, _, _ string, _ int) (string, error) {
			return `[]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_releases"
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
	if text.Text != "No releases found." {
		t.Errorf("got %q, want %q", text.Text, "No releases found.")
	}
}
