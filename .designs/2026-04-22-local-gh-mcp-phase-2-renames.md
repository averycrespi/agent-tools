# local-gh-mcp Phase 2 — Renames

Breaking-rename phase from `2026-04-21-local-gh-mcp-improvements.md`. Phase 1 (schema polish) is shipped on the `improve-gh-mcp` branch.

## Goals

1. Disambiguate the PR-vs-issue `number` parameter so an agent juggling both tool families in a single turn cannot misfire (`gh_view_issue({pr_number: 42})` and the inverse).
2. Restore the per-category naming patterns: `gh_list_*` for list tools, `gh_<verb>_run` for workflow-run tools.
3. Encode the resulting convention as a test guard so regressions cannot land silently.

## Scope

| #   | Change                              | Touches                                                                                                                                                                                                        |
| --- | ----------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 2a  | `number` → `pr_number`              | 11 PR tools: `gh_view_pr`, `gh_diff_pr`, `gh_comment_pr`, `gh_review_pr`, `gh_merge_pr`, `gh_edit_pr`, `gh_check_pr`, `gh_close_pr`, `gh_list_pr_comments`, `gh_list_pr_reviews`, `gh_list_pr_review_comments` |
| 2b  | `number` → `issue_number`           | 3 issue tools: `gh_view_issue`, `gh_comment_issue`, `gh_list_issue_comments`                                                                                                                                   |
| 2c  | `gh_check_pr` → `gh_list_pr_checks` | tool registration + handler + tests                                                                                                                                                                            |
| 2d  | `gh_rerun` → `gh_rerun_run`         | tool registration + handler + tests                                                                                                                                                                            |

## Transition

**Clean break.** Old param and tool names are removed in one release — no aliases, no deprecation warnings. The schema is the only contract. Rationale:

- Agents read schemas fresh each session, so stored memory of old names is cheap to regenerate.
- mcp-broker is effectively the only consumer; coordinated cutover is a version bump.
- Alias support would mean schema and handler disagree, defeating the schema-first design.

Breaking commits are marked with `!` per conventional commits; the version bump lands through whatever channel mcp-broker uses.

## Out of scope

Kept in later phases per the umbrella design:

- New state-transition tools (`gh_ready_pr`, `gh_draft_pr`, `gh_reopen_pr`) — Phase 3 #9.
- Removing `search` from list tools — Phase 3 #10c, bundled with its additive category.
- Go-internal identifier renames. `gh.Client.ViewPR(…, number int)` and friends keep `number` as the Go parameter name — inside a `PR*` method receiver the meaning is unambiguous, and renaming is churn.
- `internal/format/` — no schema-facing surface.
- Annotation preset changes — tools keep their existing presets; semantics unchanged.

## Implementation

### Files touched

| File                       | Change                                                                                                                                                                                                         |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/tools/pr.go`     | 11 tool defs: rename `number` → `pr_number` in JSON Schema (`properties`, `required`, description), handler argument extraction, and error strings. Rename `gh_check_pr` def + handler to `gh_list_pr_checks`. |
| `internal/tools/issue.go`  | 3 tool defs: rename `number` → `issue_number`.                                                                                                                                                                 |
| `internal/tools/run.go`    | Rename `gh_rerun` def + handler to `gh_rerun_run`.                                                                                                                                                             |
| `internal/tools/tools.go`  | Update `AllTools()` registration list.                                                                                                                                                                         |
| `internal/tools/*_test.go` | Update every fixture, schema assertion, and argument map.                                                                                                                                                      |
| `DESIGN.md`                | Update tool-inventory tables, parameter tables, validation section.                                                                                                                                            |
| `CLAUDE.md`                | Append convention line under _Conventions_ documenting the resource-qualified ID rule.                                                                                                                         |
| `README.md`                | Sweep for command examples referencing old names.                                                                                                                                                              |

### Guard test

New test in `internal/tools/tools_test.go`, sibling to `TestEveryToolHasOpenWorldHint`:

```go
func TestNoBareIDParameters(t *testing.T) {
    for _, tool := range AllTools() {
        schema := tool.Tool.InputSchema
        for _, banned := range []string{"number", "id"} {
            _, exists := schema.Properties[banned]
            require.False(t, exists,
                "tool %q exposes bare %q property; use a resource-qualified name (pr_number, issue_number, run_id, cache_id)",
                tool.Tool.Name, banned)
        }
    }
}
```

Generic rather than per-tool — any future tool author is forced to pick a resource-qualified name without us having to enumerate which tools apply.

## Rollout

### Commit sequence

1. `test(local-gh-mcp): add bare-ID parameter guard` — guard committed first, fails on every existing `number` tool. Proves the check bites.
2. `feat(local-gh-mcp)!: rename PR number params to pr_number`
3. `feat(local-gh-mcp)!: rename issue number params to issue_number`
4. `feat(local-gh-mcp)!: rename gh_check_pr to gh_list_pr_checks`
5. `feat(local-gh-mcp)!: rename gh_rerun to gh_rerun_run`
6. `docs(local-gh-mcp): document resource-qualified ID convention`

Each subsequent rename commit flips the guard closer to green. If the guard still fails after commit 4, something was missed.

Run `make audit` between commits (tidy + fmt + lint + test + govulncheck), matching the Phase 1 rhythm.

### Risks

- **Silent callers.** Hand-written system prompts or cached agent memory using `{number: 42}` or `gh_check_pr` will 400. Accepted — that is the point of the clean break.
- **Test drift.** `git grep -n '"number"'` scoped to `internal/tools/` before the rename; update call sites deliberately rather than via blanket search-and-replace.
- **Docs drift.** The guard covers code, not docs. `DESIGN.md`, `README.md`, and `CLAUDE.md` are updated in the final docs commit.

### Non-risks

- gh CLI invocations are unchanged — handlers still construct `gh pr view … 42`. Only the JSON-schema surface renames.
- Annotation semantics unchanged; no re-review of tool safety.
- mcp-broker needs no code changes; it proxies.
