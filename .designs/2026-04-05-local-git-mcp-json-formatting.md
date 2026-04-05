# local-git-mcp: Structured JSON output for list tools

**Date:** 2026-04-05
**Scope:** `local-git-mcp/internal/git` and `local-git-mcp/internal/tools`

## Problem

`git_list_remotes` and `git_list_remote_refs` return raw newline-delimited text from
`git remote -v` and `git ls-remote`. This is awkward for agents to consume — they must
parse tab-separated, annotated text instead of structured data.

## Design

### New types in `git.go`

```go
type Remote struct {
    Name     string `json:"name"`
    FetchURL string `json:"fetch_url"`
    PushURL  string `json:"push_url"`
}

type Ref struct {
    SHA string `json:"sha"`
    Ref string `json:"ref"`
}
```

### Updated method signatures

```go
func (c *Client) ListRemotes(repoPath string) ([]Remote, error)
func (c *Client) ListRemoteRefs(repoPath, remote string) ([]Ref, error)
```

`ListRemotes` parses `git remote -v` output. Each remote appears twice (fetch + push),
so results are deduplicated by name into a single `Remote` per entry.

`ListRemoteRefs` parses `git ls-remote` output. Each line is `<sha>\t<ref>`.

### Updated interface in `tools.go`

```go
import "github.com/averycrespi/agent-tools/local-git-mcp/internal/git"

type GitClient interface {
    ValidateRepo(repoPath string) error
    Push(repoPath, remote, refspec string, force bool) (string, error)
    Pull(repoPath, remote, branch string, rebase bool) (string, error)
    Fetch(repoPath, remote, refspec string) (string, error)
    ListRemoteRefs(repoPath, remote string) ([]git.Ref, error)
    ListRemotes(repoPath string) ([]git.Remote, error)
}
```

### Handler marshalling

```go
case "git_list_remotes":
    remotes, err := h.git.ListRemotes(repoPath)
    if err != nil {
        return gomcp.NewToolResultError(err.Error()), nil
    }
    out, _ := json.Marshal(remotes)
    return gomcp.NewToolResultText(string(out)), nil
```

Same pattern for `git_list_remote_refs`. Push/pull/fetch are unchanged — plain text is
appropriate for operational commands that return git's own status messages.

### Example outputs

`git_list_remotes`:
```json
[{"name":"origin","fetch_url":"git@github.com:user/repo.git","push_url":"git@github.com:user/repo.git"}]
```

`git_list_remote_refs`:
```json
[{"sha":"abc123def456...","ref":"refs/heads/main"},{"sha":"789abc...","ref":"refs/tags/v1.0"}]
```

## Test changes

- `git_test.go`: `TestListRemoteRefs_Success` and `TestListRemotes_Success` assert on
  structured `[]Ref` / `[]Remote` values instead of string containment.
- `tools_test.go`: `mockGitClient` fields updated to match new signatures; list handler
  tests assert result content is valid JSON with expected fields.
