package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/format"
	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	"github.com/google/shlex"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

// containsQualifier reports whether any token has one of the given qualifier
// prefixes (e.g. "is:", "repo:"). Tokens come pre-split via shlex so a single
// quoted phrase like `"hello world"` is a single token and won't false-match.
func containsQualifier(tokens []string, qualifiers ...string) bool {
	for _, t := range tokens {
		for _, q := range qualifiers {
			if strings.HasPrefix(t, q+":") {
				return true
			}
		}
	}
	return false
}

// containsStateQualifier reports whether any token is a GitHub search DSL
// state qualifier (`is:open`, `is:closed`, `is:merged`, or the equivalent
// `state:` forms). Other `is:*` qualifiers (`is:pr`, `is:draft`, `is:public`,
// ...) are NOT state filters and must not trigger the duplicate-filter check.
func containsStateQualifier(tokens []string) bool {
	for _, t := range tokens {
		switch t {
		case "is:open", "is:closed", "is:merged",
			"state:open", "state:closed", "state:merged":
			return true
		}
	}
	return false
}

// queryTokens shlex-splits a search query for conflict detection. On split
// failure the wrapper layer surfaces the same error with more context, so we
// return nil here and let the wrapper produce the final error.
func queryTokens(query string) []string {
	tokens, err := shlex.Split(query)
	if err != nil {
		return nil
	}
	return tokens
}

// rejectConflict returns a tool-error result when the named flag is set
// non-empty and the query already contains one of the matching qualifiers.
func rejectConflict(flag, flagValue string, tokens []string, qualifiers ...string) *gomcp.CallToolResult {
	if flagValue == "" {
		return nil
	}
	if containsQualifier(tokens, qualifiers...) {
		return gomcp.NewToolResultError(fmt.Sprintf("%s set both via flag and query; pick one", flag))
	}
	return nil
}

func (h *Handler) searchTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_search_prs",
			Description: "Search pull requests across GitHub using the GitHub search DSL. Example: 'is:open author:@me review-requested:@me'. Use gh_list_prs instead if you have a specific owner/repo. Default 30, max 100; pass `limit` to widen. Filter flags (state, repo, owner) and same-meaning qualifiers in `query` (is:, repo:, org:) are both forwarded to GitHub; setting the same filter via both flag and query is rejected.",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "GitHub search DSL query (see tool description for example).",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Filter by repository (owner/repo)",
					},
					"owner": map[string]any{
						"type":        "string",
						"description": "Filter by owner",
					},
					"state": map[string]any{
						"type":        "string",
						"enum":        []string{"open", "closed", "merged", "all"},
						"description": "Filter by state. 'closed' returns all closed PRs (including merged); use 'merged' to narrow to only merged. To exclude merged from closed, omit `state` and add `is:closed -is:merged` to `query`.",
					},
					"author": map[string]any{
						"type":        "string",
						"description": "Filter by author",
					},
					"label": map[string]any{
						"type":        "string",
						"description": "Filter by label",
					},
					"limit": map[string]any{
						"type":        "number",
						"minimum":     1,
						"default":     30,
						"description": "Max results (default 30, max 100).",
					},
					"max_body_length": map[string]any{
						"type":        "number",
						"default":     200,
						"description": "Max body excerpt length in bytes (default 200, max 500).",
					},
				},
				Required: []string{"query"},
			},
		},
		{
			Name:        "gh_search_issues",
			Description: "Search issues across GitHub using the GitHub search DSL. Example: 'is:open label:bug author:@me'. Use gh_list_issues instead if you have a specific owner/repo. Default 30, max 100; pass `limit` to widen. Filter flags (state, repo, owner) and same-meaning qualifiers in `query` (is:, repo:, org:) are both forwarded to GitHub; setting the same filter via both flag and query is rejected.",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "GitHub search DSL query (see tool description for example).",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Filter by repository (owner/repo)",
					},
					"owner": map[string]any{
						"type":        "string",
						"description": "Filter by owner",
					},
					"state": map[string]any{
						"type":        "string",
						"enum":        []string{"open", "closed", "all"},
						"description": "Filter by state.",
					},
					"author": map[string]any{
						"type":        "string",
						"description": "Filter by author",
					},
					"label": map[string]any{
						"type":        "string",
						"description": "Filter by label",
					},
					"limit": map[string]any{
						"type":        "number",
						"minimum":     1,
						"default":     30,
						"description": "Max results (default 30, max 100).",
					},
					"max_body_length": map[string]any{
						"type":        "number",
						"default":     200,
						"description": "Max body excerpt length in bytes (default 200, max 500).",
					},
				},
				Required: []string{"query"},
			},
		},
		{
			Name:        "gh_search_repos",
			Description: "Search repositories across GitHub using the GitHub search DSL. Example: 'language:go stars:>100 topic:cli'. Default 30, max 100; pass `limit` to widen. Filter flags (owner, language, topic, stars) and same-meaning qualifiers in `query` are both forwarded to GitHub; setting the same filter via both flag and query is rejected.",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "GitHub search DSL query (see tool description for example).",
					},
					"owner": map[string]any{
						"type":        "string",
						"description": "Filter by owner",
					},
					"language": map[string]any{
						"type":        "string",
						"description": "Filter by language",
					},
					"topic": map[string]any{
						"type":        "string",
						"description": "Filter by topic",
					},
					"stars": map[string]any{
						"type":        "string",
						"description": "Filter by star count (e.g. >100)",
					},
					"limit": map[string]any{
						"type":        "number",
						"minimum":     1,
						"default":     30,
						"description": "Max results (default 30, max 100).",
					},
				},
				Required: []string{"query"},
			},
		},
		{
			Name:        "gh_search_code",
			Description: "Search code across GitHub using the GitHub search DSL. Example: 'addEventListener language:javascript repo:facebook/react'. Default 30, max 100; pass `limit` to widen. Filter flags (repo, owner, language, extension, filename) and same-meaning qualifiers in `query` are both forwarded to GitHub; setting the same filter via both flag and query is rejected.",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "GitHub search DSL query (see tool description for example).",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Filter by repository (owner/repo)",
					},
					"owner": map[string]any{
						"type":        "string",
						"description": "Filter by owner",
					},
					"language": map[string]any{
						"type":        "string",
						"description": "Filter by language",
					},
					"extension": map[string]any{
						"type":        "string",
						"description": "Filter by file extension",
					},
					"filename": map[string]any{
						"type":        "string",
						"description": "Filter by filename",
					},
					"limit": map[string]any{
						"type":        "number",
						"minimum":     1,
						"default":     30,
						"description": "Max results (default 30, max 100).",
					},
				},
				Required: []string{"query"},
			},
		},
		{
			Name:        "gh_search_commits",
			Description: "Search commits across GitHub using the GitHub search DSL. Example: 'author:octocat repo:github/docs merge:false'. Default 30, max 100; pass `limit` to widen. Filter flags (repo, owner, author) and same-meaning qualifiers in `query` are both forwarded to GitHub; setting the same filter via both flag and query is rejected.",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "GitHub search DSL query (see tool description for example).",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Filter by repository (owner/repo)",
					},
					"owner": map[string]any{
						"type":        "string",
						"description": "Filter by owner",
					},
					"author": map[string]any{
						"type":        "string",
						"description": "Filter by author",
					},
					"limit": map[string]any{
						"type":        "number",
						"minimum":     1,
						"default":     30,
						"description": "Max results (default 30, max 100).",
					},
				},
				Required: []string{"query"},
			},
		},
	}
}

func (h *Handler) handleSearchPRs(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	query := stringFromArgs(args, "query")
	if query == "" {
		return gomcp.NewToolResultError("query is required"), nil
	}
	state := stringFromArgs(args, "state")
	if errResult := validateEnum("state", state, []string{"open", "closed", "merged", "all"}); errResult != nil {
		return errResult, nil
	}
	repo := stringFromArgs(args, "repo")
	owner := stringFromArgs(args, "owner")
	author := stringFromArgs(args, "author")
	label := stringFromArgs(args, "label")
	tokens := queryTokens(query)
	if state != "" && containsStateQualifier(tokens) {
		return gomcp.NewToolResultError("state set both via flag and query; pick one"), nil
	}
	if errResult := rejectConflict("repo", repo, tokens, "repo"); errResult != nil {
		return errResult, nil
	}
	if errResult := rejectConflict("owner", owner, tokens, "owner", "org"); errResult != nil {
		return errResult, nil
	}
	if errResult := rejectConflict("author", author, tokens, "author"); errResult != nil {
		return errResult, nil
	}
	if errResult := rejectConflict("label", label, tokens, "label"); errResult != nil {
		return errResult, nil
	}
	limit, errResult := validateLimit(args)
	if errResult != nil {
		return errResult, nil
	}
	opts := gh.SearchPRsOpts{
		Repo:   repo,
		Owner:  owner,
		State:  state,
		Author: author,
		Label:  label,
		Limit:  limit,
	}
	out, err := h.gh.SearchPRs(ctx, query, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.SearchPRItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return parseError("gh_search_prs", err, out), nil
	}
	maxBody := clampSearchBodyLength(intFromArgs(args, "max_body_length"))
	var lines []string
	for _, item := range items {
		lines = append(lines, format.FormatSearchPRItem(item, maxBody))
	}
	if len(lines) == 0 {
		return gomcp.NewToolResultText("No pull requests found."), nil
	}
	return gomcp.NewToolResultText(strings.Join(lines, "\n")), nil
}

func (h *Handler) handleSearchIssues(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	query := stringFromArgs(args, "query")
	if query == "" {
		return gomcp.NewToolResultError("query is required"), nil
	}
	state := stringFromArgs(args, "state")
	if errResult := validateEnum("state", state, []string{"open", "closed", "all"}); errResult != nil {
		return errResult, nil
	}
	repo := stringFromArgs(args, "repo")
	owner := stringFromArgs(args, "owner")
	author := stringFromArgs(args, "author")
	label := stringFromArgs(args, "label")
	tokens := queryTokens(query)
	if state != "" && containsStateQualifier(tokens) {
		return gomcp.NewToolResultError("state set both via flag and query; pick one"), nil
	}
	if errResult := rejectConflict("repo", repo, tokens, "repo"); errResult != nil {
		return errResult, nil
	}
	if errResult := rejectConflict("owner", owner, tokens, "owner", "org"); errResult != nil {
		return errResult, nil
	}
	if errResult := rejectConflict("author", author, tokens, "author"); errResult != nil {
		return errResult, nil
	}
	if errResult := rejectConflict("label", label, tokens, "label"); errResult != nil {
		return errResult, nil
	}
	limit, errResult := validateLimit(args)
	if errResult != nil {
		return errResult, nil
	}
	opts := gh.SearchIssuesOpts{
		Repo:   repo,
		Owner:  owner,
		State:  state,
		Author: author,
		Label:  label,
		Limit:  limit,
	}
	out, err := h.gh.SearchIssues(ctx, query, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.SearchIssueItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return parseError("gh_search_issues", err, out), nil
	}
	maxBody := clampSearchBodyLength(intFromArgs(args, "max_body_length"))
	var lines []string
	for _, item := range items {
		lines = append(lines, format.FormatSearchIssueItem(item, maxBody))
	}
	if len(lines) == 0 {
		return gomcp.NewToolResultText("No issues found."), nil
	}
	return gomcp.NewToolResultText(strings.Join(lines, "\n")), nil
}

func (h *Handler) handleSearchRepos(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	query := stringFromArgs(args, "query")
	if query == "" {
		return gomcp.NewToolResultError("query is required"), nil
	}
	owner := stringFromArgs(args, "owner")
	language := stringFromArgs(args, "language")
	topic := stringFromArgs(args, "topic")
	stars := stringFromArgs(args, "stars")
	tokens := queryTokens(query)
	if errResult := rejectConflict("owner", owner, tokens, "owner", "org", "user"); errResult != nil {
		return errResult, nil
	}
	if errResult := rejectConflict("language", language, tokens, "language"); errResult != nil {
		return errResult, nil
	}
	if errResult := rejectConflict("topic", topic, tokens, "topic"); errResult != nil {
		return errResult, nil
	}
	if errResult := rejectConflict("stars", stars, tokens, "stars"); errResult != nil {
		return errResult, nil
	}
	limit, errResult := validateLimit(args)
	if errResult != nil {
		return errResult, nil
	}
	opts := gh.SearchReposOpts{
		Owner:    owner,
		Language: language,
		Topic:    topic,
		Stars:    stars,
		Limit:    limit,
	}
	out, err := h.gh.SearchRepos(ctx, query, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.SearchRepoItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return parseError("gh_search_repos", err, out), nil
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, format.FormatSearchRepoItem(item))
	}
	if len(lines) == 0 {
		return gomcp.NewToolResultText("No repositories found."), nil
	}
	return gomcp.NewToolResultText(strings.Join(lines, "\n")), nil
}

func (h *Handler) handleSearchCode(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	query := stringFromArgs(args, "query")
	if query == "" {
		return gomcp.NewToolResultError("query is required"), nil
	}
	repo := stringFromArgs(args, "repo")
	owner := stringFromArgs(args, "owner")
	language := stringFromArgs(args, "language")
	extension := stringFromArgs(args, "extension")
	filename := stringFromArgs(args, "filename")
	tokens := queryTokens(query)
	if errResult := rejectConflict("repo", repo, tokens, "repo"); errResult != nil {
		return errResult, nil
	}
	if errResult := rejectConflict("owner", owner, tokens, "owner", "org"); errResult != nil {
		return errResult, nil
	}
	if errResult := rejectConflict("language", language, tokens, "language"); errResult != nil {
		return errResult, nil
	}
	if errResult := rejectConflict("extension", extension, tokens, "extension"); errResult != nil {
		return errResult, nil
	}
	if errResult := rejectConflict("filename", filename, tokens, "filename"); errResult != nil {
		return errResult, nil
	}
	limit, errResult := validateLimit(args)
	if errResult != nil {
		return errResult, nil
	}
	opts := gh.SearchCodeOpts{
		Repo:      repo,
		Owner:     owner,
		Language:  language,
		Extension: extension,
		Filename:  filename,
		Limit:     limit,
	}
	out, err := h.gh.SearchCode(ctx, query, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.SearchCodeItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return parseError("gh_search_code", err, out), nil
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, format.FormatSearchCodeItem(item))
	}
	if len(lines) == 0 {
		return gomcp.NewToolResultText("No code found."), nil
	}
	return gomcp.NewToolResultText(strings.Join(lines, "\n")), nil
}

func (h *Handler) handleSearchCommits(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	query := stringFromArgs(args, "query")
	if query == "" {
		return gomcp.NewToolResultError("query is required"), nil
	}
	repo := stringFromArgs(args, "repo")
	owner := stringFromArgs(args, "owner")
	author := stringFromArgs(args, "author")
	tokens := queryTokens(query)
	if errResult := rejectConflict("repo", repo, tokens, "repo"); errResult != nil {
		return errResult, nil
	}
	if errResult := rejectConflict("owner", owner, tokens, "owner", "org"); errResult != nil {
		return errResult, nil
	}
	if errResult := rejectConflict("author", author, tokens, "author"); errResult != nil {
		return errResult, nil
	}
	limit, errResult := validateLimit(args)
	if errResult != nil {
		return errResult, nil
	}
	opts := gh.SearchCommitsOpts{
		Repo:   repo,
		Owner:  owner,
		Author: author,
		Limit:  limit,
	}
	out, err := h.gh.SearchCommits(ctx, query, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.SearchCommitItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return parseError("gh_search_commits", err, out), nil
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, format.FormatSearchCommitItem(item))
	}
	if len(lines) == 0 {
		return gomcp.NewToolResultText("No commits found."), nil
	}
	return gomcp.NewToolResultText(strings.Join(lines, "\n")), nil
}
