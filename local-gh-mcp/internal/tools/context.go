package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) contextTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_whoami",
			Description: "Show the authenticated GitHub user (login, display name, profile URL). Useful for grounding `author:@me` or `review-requested:@me` search queries.",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type:       "object",
				Properties: map[string]any{},
			},
		},
	}
}

type userResponse struct {
	Login   string  `json:"login"`
	Name    *string `json:"name"`
	HTMLURL string  `json:"html_url"`
	Type    string  `json:"type"`
}

func (h *Handler) handleWhoami(ctx context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	raw, err := h.gh.ViewUser(ctx)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var u userResponse
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		return parseError("gh_whoami", err, raw), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Logged in as `%s`", u.Login)
	if u.Type == "Bot" {
		b.WriteString(" [bot]")
	}
	if u.Name != nil && *u.Name != "" {
		fmt.Fprintf(&b, " (%s)", *u.Name)
	}
	b.WriteString("\n")
	b.WriteString(u.HTMLURL)
	return gomcp.NewToolResultText(b.String()), nil
}
