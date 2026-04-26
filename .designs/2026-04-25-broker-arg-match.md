# MCP Broker Rule Argument Matching

Date: 2026-04-25
Status: Design — ready for implementation

## Motivation

The MCP broker's rule engine matches only on tool name (a `filepath.Match` glob) and emits a verdict (`allow`, `deny`, `require-approval`). This is too coarse for tools whose safety depends on what arguments they're called with. The canonical example: `git_push` to `origin` is routine; `git_push` to `production` is dangerous. Today both share the same verdict.

This design extends rules with optional argument matching so a single rule can authorize a tool only for a specific subset of calls. Rules without argument constraints behave exactly as today.

## Goals

- Allow rules to constrain on tool-call argument values, with AND semantics across multiple constraints.
- Support nested fields and array elements via a simple path syntax.
- Support exact-string and regex matchers.
- Preserve all existing rule semantics: first-match-wins, fail-closed default of `require-approval`.
- Keep the dashboard rules tab honest about what each rule actually does.

## Non-goals (deferrable to follow-ups)

- Wildcard or recursive path segments (`*`, `**`).
- `anyOf` matcher (regex alternation covers it for v1).
- Persisting matched rule index in the audit table.
- Interactive "test these args against the rules" dashboard panel.

## Configuration

A rule gains one optional field, `args`. When missing or empty, the rule matches on tool name alone — fully backward compatible with existing configs.

```json
{
  "tool": "git_push",
  "verdict": "allow",
  "args": [
    { "path": "remote", "match": "origin" },
    { "path": "commit.message", "match": { "regex": "^feat:" } }
  ]
}
```

Each entry of `args` is an **argument pattern**:

- `path` — string. Dot-separated. Each segment is either a key (string) or an integer index (for arrays). Examples: `remote`, `commit.message`, `command.0`.
- `match` — required. Either a bare JSON string (exact match) or an object `{ "regex": "<RE2>" }`.

A rule matches a call iff:

1. The rule's tool glob matches the tool name (existing behavior).
2. **Every** pattern in `args` resolves and matches.

Anything else → rule fails to match → evaluation continues to the next rule.

## Path resolution

`path` is parsed at engine construction into a sequence of segments. Each segment is either a string key or an integer index. Resolution walks the call's `map[string]any` linearly:

| Current node     | Segment kind | Behavior                     |
| ---------------- | ------------ | ---------------------------- |
| `map[string]any` | key          | descend; missing key → fail  |
| `[]any`          | index        | descend; out-of-range → fail |
| any other type   | any          | fail                         |

If resolution fails for any reason (missing key, type mismatch, out-of-range index), the pattern fails → the rule fails → evaluation moves on.

The resolved value is stringified before matching: `encoding/json.Marshal`, then strip surrounding quotes for plain strings. So `42` → `"42"`, `true` → `"true"`, `null` → `"null"`. Authors who want to match into containers should use deeper paths to reach scalars; matching against a marshaled object literal is allowed but rarely useful.

Path syntax errors at config load:

- empty segments (`a..b`)
- entirely empty path (`""`)

These are surfaced as configuration errors at engine construction.

## Matchers

Two matcher kinds for v1:

```go
type argMatcher interface{ match(value string) bool }

type exactMatcher struct{ want string }
type regexMatcher struct{ re *regexp.Regexp }
```

Decoding from JSON `match`:

- bare string → `exactMatcher{want: s}`
- object with exactly one key `regex` (string value) → `regexMatcher{re: regexp.MustCompile(...)}`
- anything else → configuration error

**Regex semantics:** Go's `regexp` package (RE2). **Author-controlled anchoring** — we do not auto-anchor. A rule `{"regex": "origin"}` matches `"my-origin-fork"`. This is documented as a footgun in `DESIGN.md` and the README; authors should anchor with `^...$` when they want full-match semantics. We considered auto-wrapping with `^(?:...)$` and rejected it: it deviates from regex conventions and surprises authors. Revisit if real-world misuse appears.

`anyOf` is deferred. Rule authors needing alternation use regex: `{"regex": "^(main|develop|release/.+)$"}`. Adding `anyOf` later under `match` is non-breaking (new shape, same field).

## Evaluation algorithm

The engine loop gains one gate. Today (`internal/rules/rules.go:71-80`):

```go
for i, rule := range e.rules {
    matched, err := filepath.Match(rule.Tool, tool)
    if err != nil { continue }
    if matched { return ParseVerdict(rule.Verdict), i }
}
return RequireApproval, -1
```

After:

```go
for i, rule := range e.rules {
    nameMatched, err := filepath.Match(rule.Tool, tool)
    if err != nil || !nameMatched { continue }
    if !rule.argsMatch(args) { continue }   // NEW
    return ParseVerdict(rule.Verdict), i
}
return RequireApproval, -1
```

Signature changes:

- `Engine.Evaluate(tool string)` → `Evaluate(tool string, args map[string]any)`
- `Engine.EvaluateWithRule(tool string)` → `EvaluateWithRule(tool string, args map[string]any)`

The broker (`internal/broker/broker.go:57`) already has `args map[string]any` at the call site and just forwards it. This is the only public-API change; the package is `internal/`, so there is no external breakage.

When `rule.Args` is empty, `argsMatch` returns true unconditionally — legacy rule behavior is byte-identical to today.

Properties preserved:

- **First match wins.** Arg matching is just an extra gate before "this rule fires."
- **Fail-closed default.** Tool calls no rule fully matches still fall through to `RequireApproval`.
- **Deterministic and stateless.** Pure function of `(tool, args, ruleset)`. Matchers compiled once at engine construction.

A subtle but important consequence: a deny rule with args is narrow. `{"tool": "git_push", "verdict": "deny", "args": [{"path": "remote", "match": "production"}]}` denies `git_push` only when `remote=production`; other calls fall through to subsequent rules. Authors wanting "deny everything except origin" still write the broad-then-narrow pattern (specific allow first, broad deny after). Arg matching does not change this.

## Go types and package layout

All changes live under `mcp-broker/internal/`. No new external dependencies; `regexp` is stdlib.

**`internal/config/config.go`** — extend `RuleConfig`:

```go
type RuleConfig struct {
    Tool    string         `json:"tool"`
    Verdict string         `json:"verdict"`
    Args    []ArgPattern   `json:"args,omitempty"`
}

type ArgPattern struct {
    Path  string          `json:"path"`
    Match json.RawMessage `json:"match"`
}
```

`Match` stays as `json.RawMessage` at the config layer so config decoding doesn't depend on the rules package. The `Load()` function continues to do shallow validation only; structural and regex compilation errors surface in `NewEngine`.

**`internal/rules/rules.go`** — add compiled types:

```go
type compiledRule struct {
    raw     config.RuleConfig
    verdict Verdict
    args    []compiledPattern
}

type compiledPattern struct {
    segments []pathSegment
    matcher  argMatcher
}

type pathSegment struct {
    key   string  // empty if index segment
    index int     // -1 if key segment
}
```

**New file: `internal/rules/match.go`** — path parsing, resolution, and matcher implementations. Keeps `rules.go` focused on the engine loop and verdict parsing.

`Engine.Rules()` continues to return raw `[]config.RuleConfig` — dashboards and other consumers shouldn't see the compiled form.

**Validation at `NewEngine`:**

- Parse each pattern's `path`; reject empty/empty-segment paths.
- Decode each pattern's `match`; reject anything other than a string or `{regex: <string>}`.
- Compile regexes; surface `regexp.Compile` errors.

Errors wrap as `fmt.Errorf("rule %d: args[%d]: %w", ruleIdx, patIdx, err)` per the existing CLAUDE.md error-wrapping convention. This is a minor departure from today's lazy validation (glob errors are skipped at evaluation time) — eager validation is the right move for regex compilation, where deferring to evaluation would log surprising failures during traffic instead of at startup.

## Audit and observability — out of scope for v1

The current audit `Record` (`internal/audit/audit.go:18-26`) does not persist a matched rule index, and adding one would mean a new field, a new SQLite column, and a second `ALTER TABLE` migration alongside the existing `denial_reason` migration. None of that is needed for arg matching to work — `Tool`, `Args`, and `Verdict` already capture what happened.

For v1 we leave the audit schema alone. `EvaluateWithRule` already returns the matched index; the broker can emit it via the structured logger as a small one-line change with no schema cost. Persisting it is a follow-up if the dashboard ever wants to render "which rule fired" in the audit tab.

## Dashboard rules tab

Today's `GET /api/rules` returns `{index, tool, verdict, matches[]}` for each rule, plus a flat "unmatched tools" list. The rule card in `index.html` renders a verdict pill, the tool glob, and the list of name-matching tools beneath. With arg matching present, this view becomes misleading: a tool listed under a constrained rule may not actually fire that rule's verdict.

**API change** — extend `ruleView` to pass arg patterns through:

```go
type ruleView struct {
    Index   int                  `json:"index"`
    Tool    string               `json:"tool"`
    Verdict string                `json:"verdict"`
    Matches []string             `json:"matches"`
    Args    []config.ArgPattern  `json:"args,omitempty"`
}
```

The dashboard already has `Rules() []config.RuleConfig` via `RulesLister`; no new plumbing required.

**Rule card UI** — replace the verdict-pill + tool-glob layout with a sentence-like headline. Patterns always stack one per line below the headline; no inline mode, no length thresholds.

No args:

```
allow github.search_*
```

With args (every arg case looks like this — uniform layout):

```
allow git_push with
  remote = "origin"
  commit.message ~ "^feat:"
```

Symbols: `=` for exact, `~` for regex. The trailing `with` cues that constraints follow. The existing tool-name list remains beneath, unchanged.

**Fall-through area** — split into two side-by-side boxes:

- **Always fall through** — tool names whose glob matches no rule. Today's "Unmatched tools" set, renamed.
- **May fall through (depending on arguments)** — tool names whose entire chain of name-matching rules is constrained, so verdict depends on args. Computed by walking the rule list per tool and classifying it here only if no unconstrained rule ever name-matches.

Both boxes carry a caption naming the default verdict they fall through to. Wording:

- Always box: "These tools fall through to the default verdict: `require-approval`."
- May-fall-through box: "These tools may fall through to the default verdict: `require-approval`, depending on call arguments."

Side-by-side layout uses flex; on narrow viewports the boxes stack (matching existing dashboard responsive conventions). Empty boxes still render their heading and caption with a muted "none" placeholder, so the layout stays symmetric.

The strict reading — "may fall through" means specifically "could reach the default verdict" — is intentional. A tool whose first matching rule is constrained but whose later matching rule is unconstrained is _not_ at risk of the default; it's just bouncing between known verdicts. Conflating that with default-fall-through risk would muddy the safety question the section answers.

## Testing strategy

Extends existing table-driven patterns in `internal/rules/rules_test.go`.

**Path resolution** (new tests in `match_test.go`): table of `(args map, path string, want string, wantOK bool)` covering top-level keys, nested objects, integer indices, missing keys, type mismatches, null values, non-scalar leaves.

**Matchers** (new): exact (string equality, non-string stringified, no match) and regex (anchored, unanchored substring footgun explicitly tested as a "this is the documented behavior" test, invalid regex rejected at engine construction).

**Engine integration** (extend `rules_test.go`):

- Rule with empty `args` behaves identically to a today-style rule.
- Rule with all-passing arg patterns matches.
- Rule with one failing arg pattern falls through.
- First-match-wins still holds when the first name-matching rule has args that fail.
- Multiple AND patterns required to match.
- Path failure (missing key) → rule does not match.

**Broker integration** (`internal/broker/broker_test.go`): one end-to-end test that an arg-constrained allow rule triggers, and one that an arg-constrained deny rule blocks.

**Dashboard** (`internal/dashboard/dashboard_test.go`): rule with `args` populated produces a `ruleView` with `Args` set; tool whose only matching rule is constrained appears in "may fall through" but not "always fall through"; tool with no matching rule appears in "always fall through" only.

## Documentation updates

- `mcp-broker/DESIGN.md` — extend the "Rules engine" section to document optional `args`, the dotted+index path syntax, exact and regex matchers, AND semantics across patterns, fail-closed behavior on path failures, and the regex anchoring footgun. Include a worked example.
- `mcp-broker/README.md` — add an arg-matching example to the rules section.

## Summary of locked-in decisions

| Decision           | Choice                                                         |
| ------------------ | -------------------------------------------------------------- |
| `args` semantics   | optional, AND across patterns, subset (extra args ignored)     |
| Path syntax        | dotted, integer indices for arrays, no wildcards in v1         |
| Matchers           | exact string, `{regex: ...}`. No `anyOf` in v1                 |
| Regex anchoring    | author-controlled (no auto-anchor)                             |
| Value types        | stringify via JSON, then match                                 |
| Missing path       | pattern fails → rule fails                                     |
| Default verdict    | unchanged: `require-approval`                                  |
| Audit schema       | unchanged in v1                                                |
| Dashboard headline | always-stacked patterns, `with` keyword, `=`/`~` symbols       |
| Fall-through area  | two side-by-side boxes, strict "could reach default" semantics |
