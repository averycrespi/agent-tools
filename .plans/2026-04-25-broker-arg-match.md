# MCP Broker Rule Argument Matching Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Extend the mcp-broker rules engine so a rule can constrain on tool-call argument values (dotted path + exact-string or regex matcher), with AND semantics across constraints, while preserving every existing rule semantic.

**Architecture:** A new `Args []ArgPattern` field on `RuleConfig` (config layer). At engine construction (`rules.New`), each pattern is parsed and compiled — the path becomes a `[]pathSegment`, the matcher becomes either an `exactMatcher` or `regexMatcher`. Evaluation gains one extra gate after the existing tool-name glob match: every compiled pattern must resolve a value from the call's `args map[string]any` and that value (after JSON stringification) must match. Path failures, type mismatches, and any non-matching pattern cause the rule to fail and evaluation to continue. The dashboard's rules tab is updated to render constraint-aware headlines and to split fall-through tools into "always falls through to default" and "may fall through depending on arguments."

**Tech Stack:** Go (stdlib `encoding/json`, `regexp`, `path/filepath`), `mcp-broker/internal/{config,rules,broker,dashboard}`, embedded HTML/CSS/JS in `internal/dashboard/index.html`. No new dependencies.

**Source of truth:** `.designs/2026-04-25-broker-arg-match.md`. Read it before starting — every decision (regex anchoring, value stringification, AND semantics, dashboard wording) is locked in there. When the plan and the design disagree, the design is right; flag the discrepancy.

**Conventions reminders:**

- Errors wrap with context per `mcp-broker/CLAUDE.md`: `fmt.Errorf("doing X: %w", err)`.
- Conventional commits, imperative mood, ≤50 chars, no trailing period (per global `~/.claude/CLAUDE.md`).
- Run `make audit` before each commit (tidy + fmt + lint + test + govulncheck). Run from `mcp-broker/`.
- File paths in this plan are repo-root-relative — never hardcode an absolute worktree path.

---

### Task 1: Config types for argument patterns

**Files:**

- Modify: `mcp-broker/internal/config/config.go` (extend `RuleConfig`, add `ArgPattern`)
- Modify: `mcp-broker/internal/config/config_test.go` (round-trip and back-compat tests)

**What to do:**

Extend `RuleConfig` and add `ArgPattern` exactly as specified in the design (§ "Go types and package layout"):

```go
// RuleConfig defines a policy rule mapping a tool glob to a verdict.
// Args, when non-empty, additionally constrains the rule to tool calls whose
// arguments satisfy every pattern.
type RuleConfig struct {
    Tool    string       `json:"tool"`
    Verdict string       `json:"verdict"`
    Args    []ArgPattern `json:"args,omitempty"`
}

// ArgPattern constrains a rule to tool calls where the value at Path matches.
// Match is either a JSON string (exact match) or {"regex": "<RE2>"}.
// It stays as RawMessage so the config package does not depend on the rules
// package; structural and regex validation happen in rules.New.
type ArgPattern struct {
    Path  string          `json:"path"`
    Match json.RawMessage `json:"match"`
}
```

The config package only needs to decode JSON cleanly. **Do not** add structural validation here — that belongs to `rules.New`. `Match` stays `json.RawMessage` deliberately (design § Go types).

`omitempty` on `Args` is required so existing configs with no `args` field re-serialize byte-identically (`Refresh` path).

**Acceptance Criteria:**

- A config file with a rule containing `args` (mix of exact-string and `{regex: ...}` matches) round-trips through `Load` → `Save` and back unchanged in semantics.
- A config file with rules that have no `args` field (today's shape) round-trips with the `args` key absent in the output (verify via direct JSON unmarshal of the saved file, not just struct equality).
- Tests cover: rule with no args, rule with one exact-string arg, rule with mixed exact + regex args, regex match left as raw `json.RawMessage` containing `{"regex":"^feat:"}`.

**Notes:**

- Do not import the `rules` package from `config` — keep the dependency direction one-way. `Match` as `json.RawMessage` is what makes that possible.
- Existing `DefaultConfig()` does not need updating; the default catchall rule has no args.

**Commit:** `feat(mcp-broker): add args field to rule config`

---

### Task 2: Path parsing, resolution, and matchers

**Files:**

- Create: `mcp-broker/internal/rules/match.go`
- Create: `mcp-broker/internal/rules/match_test.go`

**What to do:**

Self-contained, pure functions in a new file. No engine touch yet — task 3 wires this in.

Define the internal types and functions in `match.go`:

```go
package rules

import (
    "encoding/json"
    "fmt"
    "regexp"
    "strconv"
    "strings"
)

// pathSegment is one step of a compiled path. Exactly one of (key, index) is
// meaningful: index == -1 means "key segment", otherwise it is an array index.
type pathSegment struct {
    key   string
    index int
}

// argMatcher matches a stringified value against a pattern.
type argMatcher interface {
    match(value string) bool
}

type exactMatcher struct{ want string }

func (m exactMatcher) match(v string) bool { return v == m.want }

type regexMatcher struct{ re *regexp.Regexp }

func (m regexMatcher) match(v string) bool { return m.re.MatchString(v) }

// compiledPattern is one validated arg constraint.
type compiledPattern struct {
    segments []pathSegment
    matcher  argMatcher
}

// parsePath parses a dotted path into segments. Numeric segments become array
// indices; non-numeric segments are keys. Empty paths and empty segments are
// rejected.
func parsePath(p string) ([]pathSegment, error) {
    if p == "" {
        return nil, fmt.Errorf("empty path")
    }
    parts := strings.Split(p, ".")
    segs := make([]pathSegment, 0, len(parts))
    for i, part := range parts {
        if part == "" {
            return nil, fmt.Errorf("empty segment at position %d", i)
        }
        if n, err := strconv.Atoi(part); err == nil {
            segs = append(segs, pathSegment{index: n})
            continue
        }
        segs = append(segs, pathSegment{key: part, index: -1})
    }
    return segs, nil
}

// resolvePath walks args along segments and returns the leaf value plus ok.
// Missing keys, out-of-range indices, and type mismatches all return ok=false.
func resolvePath(args map[string]any, segs []pathSegment) (any, bool) {
    var cur any = args
    for _, s := range segs {
        if s.index < 0 {
            // key segment
            m, ok := cur.(map[string]any)
            if !ok {
                return nil, false
            }
            v, ok := m[s.key]
            if !ok {
                return nil, false
            }
            cur = v
        } else {
            // index segment
            arr, ok := cur.([]any)
            if !ok {
                return nil, false
            }
            if s.index < 0 || s.index >= len(arr) {
                return nil, false
            }
            cur = arr[s.index]
        }
    }
    return cur, true
}

// stringifyValue converts a resolved JSON value to the string form the matcher
// sees. Plain strings are returned without surrounding quotes; everything else
// goes through json.Marshal so 42 -> "42", true -> "true", null -> "null", and
// objects/arrays produce their compact JSON form.
func stringifyValue(v any) string {
    if s, ok := v.(string); ok {
        return s
    }
    b, err := json.Marshal(v)
    if err != nil {
        return fmt.Sprintf("%v", v)
    }
    return string(b)
}

// decodeMatcher turns a RawMessage `match` field into an argMatcher.
// Accepts either a JSON string (exact) or {"regex": "<pattern>"}.
func decodeMatcher(raw json.RawMessage) (argMatcher, error) {
    if len(raw) == 0 {
        return nil, fmt.Errorf("missing match")
    }
    // Try string first.
    var s string
    if err := json.Unmarshal(raw, &s); err == nil {
        return exactMatcher{want: s}, nil
    }
    // Try regex object: must have exactly one key, "regex", with a string value.
    var obj map[string]json.RawMessage
    if err := json.Unmarshal(raw, &obj); err != nil {
        return nil, fmt.Errorf("match must be a string or {\"regex\": \"...\"}: %w", err)
    }
    if len(obj) != 1 {
        return nil, fmt.Errorf("match object must have exactly one key (regex)")
    }
    rxRaw, ok := obj["regex"]
    if !ok {
        return nil, fmt.Errorf("match object key must be \"regex\"")
    }
    var pattern string
    if err := json.Unmarshal(rxRaw, &pattern); err != nil {
        return nil, fmt.Errorf("regex value must be a string: %w", err)
    }
    re, err := regexp.Compile(pattern)
    if err != nil {
        return nil, fmt.Errorf("compiling regex %q: %w", pattern, err)
    }
    return regexMatcher{re: re}, nil
}

// matchValue resolves segs against args, stringifies the leaf, and runs matcher.
// Any failure (path miss, type mismatch) returns false.
func (p compiledPattern) matchValue(args map[string]any) bool {
    v, ok := resolvePath(args, p.segments)
    if !ok {
        return false
    }
    return p.matcher.match(stringifyValue(v))
}
```

Tests in `match_test.go` are table-driven and cover the design's testing checklist (§ Testing strategy → "Path resolution" + "Matchers"):

- `parsePath`: top-level keys, nested keys, integer indices, mixed (`commit.files.0.path`), rejects empty path, rejects empty segment (`a..b`).
- `resolvePath`: top-level lookup, nested lookup, integer-index into array, missing key → `ok=false`, missing index → `ok=false`, type mismatch (asking for key on an array) → `ok=false`, leaf is non-scalar (returns container, ok=true).
- `stringifyValue`: string passthrough (no quotes), number, bool, null, nested object (marshaled JSON form).
- `decodeMatcher`: bare string → `exactMatcher`, `{"regex":"..."}` → `regexMatcher`, rejects `{"regex": 1}`, rejects `{"foo": "bar"}`, rejects `{"regex": "x", "extra": "y"}` (multi-key), rejects bare numbers/bools, rejects `[]`, rejects malformed regex.
- `exactMatcher.match`: string equality only.
- `regexMatcher.match`: anchored pattern matches/doesn't match, **unanchored substring footgun** explicitly tested as a "this is the documented behavior" test (e.g. regex `origin` matches `my-origin-fork`) with a comment stating it's intentional per design § Matchers.
- `compiledPattern.matchValue`: end-to-end happy path, returns false on path failure, returns false on matcher failure.

**Acceptance Criteria:**

- All new tests pass under `go test -race ./internal/rules/...`.
- `go vet` and `golangci-lint run` clean for the new file.
- The unanchored-regex footgun has an explicit test asserting it matches the substring case, with a comment referencing the design's "author-controlled anchoring" decision.

**Notes:**

- Keep `match.go` package-private (lowercase types). The only exported surface for this plan lives in task 3.
- Do not auto-anchor regexes. The design explicitly considered and rejected `^(?:...)$` wrapping.
- `compiledPattern.matchValue` taking `args map[string]any` (not `any`) is intentional — the engine always starts walking from a map.

**Commit:** `feat(mcp-broker): add path matching primitives for rules`

---

### Task 3: Wire arg matching into the rules engine

**Files:**

- Modify: `mcp-broker/internal/rules/rules.go` (compiled rules, validating constructor, signature change)
- Modify: `mcp-broker/internal/rules/rules_test.go` (update existing tests, add arg-matching tests)
- Modify: `mcp-broker/internal/broker/broker.go` (forward args to evaluate)
- Modify: `mcp-broker/internal/broker/broker_test.go` (update existing call sites + add two arg-rule tests)
- Modify: `mcp-broker/internal/broker/integration_test.go` (update `rules.New` call site)
- Modify: `mcp-broker/internal/dashboard/dashboard.go` (call site if any — verify; today it consumes only `Rules()`, so likely no change)
- Modify: `mcp-broker/internal/dashboard/dashboard_test.go` (`engine := rules.New(...)` is used in `TestHandleRules_AgreesWithEngineEvaluateWithRule` and must adopt the new error-returning constructor; the `EvaluateWithRule` call there gains an `args` param — pass `nil`)
- Modify: `mcp-broker/cmd/mcp-broker/serve.go` (handle the new error from `rules.New`)

**What to do:**

The full set of cross-package changes must land in one commit because the signatures change and the build will not pass otherwise.

**1. Constructor — `rules.New` returns `(*Engine, error)`.** Compile every rule eagerly:

```go
type compiledRule struct {
    raw     config.RuleConfig
    verdict Verdict
    args    []compiledPattern
}

type Engine struct {
    rules    []config.RuleConfig // raw, returned by Rules()
    compiled []compiledRule
}

// New creates a rules engine, compiling each rule's argument patterns.
// Returns an error if any path is malformed or any regex fails to compile.
// Tool-name glob errors are still tolerated at evaluation time (preserves
// current behavior for malformed globs).
func New(rs []config.RuleConfig) (*Engine, error) {
    compiled := make([]compiledRule, len(rs))
    for i, r := range rs {
        cr := compiledRule{raw: r, verdict: ParseVerdict(r.Verdict)}
        for j, ap := range r.Args {
            segs, err := parsePath(ap.Path)
            if err != nil {
                return nil, fmt.Errorf("rule %d: args[%d]: path: %w", i, j, err)
            }
            m, err := decodeMatcher(ap.Match)
            if err != nil {
                return nil, fmt.Errorf("rule %d: args[%d]: %w", i, j, err)
            }
            cr.args = append(cr.args, compiledPattern{segments: segs, matcher: m})
        }
        compiled[i] = cr
    }
    return &Engine{rules: rs, compiled: compiled}, nil
}
```

**2. Evaluation gains `args`** (design § Evaluation algorithm):

```go
// Evaluate returns the verdict for the given tool name and arguments.
func (e *Engine) Evaluate(tool string, args map[string]any) Verdict {
    v, _ := e.EvaluateWithRule(tool, args)
    return v
}

// EvaluateWithRule returns the verdict and the index of the rule that fired,
// or (RequireApproval, -1) if no rule matches.
func (e *Engine) EvaluateWithRule(tool string, args map[string]any) (Verdict, int) {
    for i, cr := range e.compiled {
        nameMatched, err := filepath.Match(cr.raw.Tool, tool)
        if err != nil || !nameMatched {
            continue
        }
        if !argsMatch(cr.args, args) {
            continue
        }
        return cr.verdict, i
    }
    return RequireApproval, -1
}

// argsMatch returns true when every compiled pattern resolves and matches.
// Empty patterns slice → true (legacy rule behavior).
func argsMatch(patterns []compiledPattern, args map[string]any) bool {
    for _, p := range patterns {
        if !p.matchValue(args) {
            return false
        }
    }
    return true
}
```

`Engine.Rules()` continues to return `[]config.RuleConfig` (raw form) — dashboards see the configured shape, not the compiled form.

**3. Broker forwarding** (`internal/broker/broker.go:65`): change

```go
verdict := b.rules.Evaluate(tool)
```

to

```go
verdict := b.rules.Evaluate(tool, args)
```

The broker already has `args` at that scope.

**4. Update all call sites and tests.** Search results show these places use `rules.New` or `Evaluate*`:

- `cmd/mcp-broker/serve.go:106` — `engine := rules.New(cfg.Rules)` becomes:
  ```go
  engine, err := rules.New(cfg.Rules)
  if err != nil {
      return fmt.Errorf("compiling rules: %w", err)
  }
  ```
- `internal/broker/broker_test.go` — every `rules.New(...)` call must take `(engine, err)` and `require.NoError(t, err)`. Today's tests calling `b.Handle(...)` continue to work because the broker layer wraps the args.
- `internal/broker/integration_test.go:27` — same treatment.
- `internal/rules/rules_test.go` — every test must adopt the new constructor signature and pass an extra `args` argument to `Evaluate` / `EvaluateWithRule`. For tests that do not care about args, pass `nil`.
- `internal/dashboard/dashboard_test.go:338, 372` — same treatment.

**5. New tests in `internal/rules/rules_test.go`** (design § Testing strategy → "Engine integration"):

- Rule with empty `args` behaves identically to a today-style rule (allow rule fires, deny rule fires).
- Rule with all-passing arg patterns matches and returns its verdict.
- Rule with one failing arg pattern is skipped; evaluation falls through to next rule.
- First-match-wins still holds: when the first name-matching rule has args that fail and the second name-matching rule has no args, the second wins.
- Multiple AND patterns: rule with two args fires only when both match.
- Path resolution failure (missing key) → rule does not match → fall-through.
- Regex matcher integration: a rule with `{regex: "^feat:"}` fires on `"feat: x"` and not on `"fix: x"`.
- Constructor errors: bad path (`""`, `"a..b"`) and bad regex return errors from `New`.

**6. New tests in `internal/broker/broker_test.go`** (design § Testing strategy → "Broker integration"):

- Arg-constrained allow rule triggers (call args satisfy the constraint → backend Call invoked, `verdict=allow` recorded).
- Arg-constrained deny rule blocks (call args satisfy a deny constraint → `ErrDenied`, no backend Call).

Use the existing `mockServerManager` / `mockAuditLogger` infrastructure. Patterns mirror `TestBroker_Handle_AllowedTool` / `TestBroker_Handle_DeniedTool`.

**Acceptance Criteria:**

- `make audit` (run from `mcp-broker/`) passes — including `go test -race ./...` and `make test-integration`.
- All existing tests still assert the same behavior, just via the new signatures.
- A rule with no `args` produces byte-identical evaluation behavior to today (verifiable by the unchanged existing tests passing without behavioral edits).
- Bad config (empty path / bad regex) surfaces from `rules.New`, not from `Evaluate`. Add an explicit test for each.

**Notes:**

- This is the central, risky change. Update every call site in this commit; partial commits will not build.
- Keep `Engine.Rules()` returning the raw `[]config.RuleConfig` — dashboards depend on this in task 4.
- The malformed-glob behavior in `dashboard_test.go:TestHandleRules_MalformedGlobPattern` is preserved by design — `filepath.Match` errors continue to be skipped at evaluation time. Only path/regex errors are eagerly reported.
- `serve.go` already wraps errors with `fmt.Errorf("...: %w", err)` style — match it.

**Commit:** `feat(mcp-broker): match rules on tool-call arguments`

---

### Task 4: Dashboard rules API — args + fall-through classification

**Files:**

- Modify: `mcp-broker/internal/dashboard/dashboard.go` (extend `ruleView`, split unmatched into two buckets)
- Modify: `mcp-broker/internal/dashboard/dashboard_test.go` (assert new fields and bucketing)

**What to do:**

In `handleRules`:

**1. Extend `ruleView` to surface `args`** (design § Dashboard rules tab → API change):

```go
type ruleView struct {
    Index   int                 `json:"index"`
    Tool    string              `json:"tool"`
    Verdict string              `json:"verdict"`
    Matches []string            `json:"matches"`
    Args    []config.ArgPattern `json:"args,omitempty"`
}
```

Populate `Args` from `r.Args`.

**2. Replace the single `unmatched` slice with two slices.** Per design § Dashboard rules tab → Fall-through area, classify each tool by walking the rule list:

- `always` — no rule's tool glob matches this tool name. Today's "unmatched" set, renamed.
- `may` — at least one rule's glob matches the tool name, but every such rule has non-empty `args`. So whether the tool actually fires that rule depends on call arguments, and it _may_ end up at the default verdict.
- otherwise — at least one unconstrained rule name-matches this tool, so the default verdict is unreachable for this tool. Such tools belong in the existing `Matches` list of the first unconstrained rule that name-matches (today's behavior, applied with the new "first unconstrained" rule).

A subtle point: today, `Matches` lists tools under the **first name-matching rule**. With args, we need the **first name-matching rule whose args are empty** for the "where does this tool definitely land" assignment. A tool whose only name-matching rules are all constrained does _not_ go in any rule's `Matches` (because we cannot say from a static read whether any rule fires); it goes in `may`.

Algorithm per tool name:

```
firstUnconstrainedIdx = -1
sawNameMatch = false
for i, r := range rules:
    matched, err := filepath.Match(r.Tool, tool.Name)
    if err != nil || !matched:
        continue
    sawNameMatch = true
    if len(r.Args) == 0:
        firstUnconstrainedIdx = i
        break
if firstUnconstrainedIdx >= 0:
    views[firstUnconstrainedIdx].Matches = append(..., tool.Name)
else if sawNameMatch:
    may = append(may, tool.Name)
else:
    always = append(always, tool.Name)
```

Sort all three slices.

**3. Update the response shape** (rename `unmatched` and add `may_fall_through`):

```go
_ = json.NewEncoder(w).Encode(map[string]any{
    "rules":            views,
    "always_fall_through": always,
    "may_fall_through":    may,
    "default_verdict":     "require-approval",
})
```

The `unmatched` key is dropped. This is an internal API; the dashboard UI is the only consumer.

**4. Tests** (design § Testing strategy → Dashboard):

Update existing tests:

- `TestHandleRules_GroupsToolsByMatchingRule`: rename `unmatched` → `always_fall_through`, add `may_fall_through` (empty here).
- `TestHandleRules_EmptyRules`: same rename; `may_fall_through` empty.
- `TestHandleRules_RuleWithNoMatches`: same rename.
- `TestHandleRules_NilLister`: same rename.
- `TestHandleRules_MalformedGlobPattern`: same rename. (The malformed glob is silently skipped per existing behavior — preserve it.)
- `TestHandleRules_AgreesWithEngineEvaluateWithRule`: same rename; pass `nil` args to `EvaluateWithRule`.

Add new tests:

- `TestHandleRules_PassesThroughArgs`: a rule with `args: [{path:"remote", match:"origin"}]` produces a `ruleView` whose `Args` field contains exactly that pattern.
- `TestHandleRules_MayFallThrough`: rules `[{tool:"git_push", args:[{path:"remote", match:"origin"}], verdict:"allow"}, {tool:"github.*", verdict:"allow"}]`, tool list `[git_push, github.list_prs]`. `git_push` ends up in `may_fall_through` (its only name-matching rule is constrained); `github.list_prs` ends up in `Matches` of rule index 1.
- `TestHandleRules_AlwaysFallThrough`: tool with no name-matching rule appears in `always_fall_through` only.
- `TestHandleRules_ConstrainedThenUnconstrained`: rules `[{tool:"git_push", args:[...], verdict:"allow"}, {tool:"git_*", verdict:"deny"}]`, tool `git_push`. Goes in rule index 1's `Matches` (the first unconstrained name-match), not in `may_fall_through`.

**Acceptance Criteria:**

- `make test` passes; new tests cover the four classification cases (always / may / first-unconstrained / args passthrough).
- `ruleView.Args` is omitted from JSON when the rule has no args (`omitempty`), matching today's wire shape for unconstrained rules.
- The response keys are exactly `rules`, `always_fall_through`, `may_fall_through`, `default_verdict`.

**Notes:**

- Reuse `filepath.Match` here; do not depend on the `rules` package internals. The dashboard already classifies via name-glob, which is consistent with the engine's first-pass.
- Keep both fall-through slices non-nil (`[]string{}` not `nil`) so JSON encodes `[]` instead of `null`, matching today's `unmatched` behavior.

**Commit:** `feat(mcp-broker): expose arg patterns and fall-through classes`

---

### Task 5: Dashboard rules tab UI

**Files:**

- Modify: `mcp-broker/internal/dashboard/index.html` (CSS for stacked args + side-by-side fall-through; rendering logic in `loadRules`)

**What to do:**

Replace the rule card and fall-through sections with the design's layout (§ Dashboard rules tab).

**1. Rule card headline.** Replace the current `rule-header` (`#1` index pill + verdict pill + tool glob in monospace) with a sentence-style headline plus stacked patterns below. No inline mode.

No-args rule:

```
allow github.search_*
```

With-args rule (every arg case looks like this — uniform layout):

```
allow git_push with
  remote = "origin"
  commit.message ~ "^feat:"
```

Symbols:

- `=` for exact-string matchers (`Match` decodes to a JSON string).
- `~` for regex matchers (`Match` decodes to `{"regex":"..."}`).

The dashboard receives raw `Args` (each `{path, match}` where `match` is `RawMessage`-shaped JSON). In JS, decide between `=` and `~` by inspecting the parsed `match` value: a string → exact (`=`), an object with `regex` → regex (`~`). Render the value in quotes.

The trailing `with` keyword (verbatim, lowercase) cues that constraints follow. Patterns stack one per line, indented under the headline.

The existing tool-name list (`rule.matches`) remains beneath the headline, unchanged. Keep the rule index (`#1`, `#2`, …) somewhere visible — small monospace prefix on the headline is fine.

Verdict color classes (`verdict-allow`, `verdict-deny`, `verdict-require-approval`) continue to apply to the verdict word.

**2. Fall-through section.** Replace the single "Unmatched tools" panel with two side-by-side boxes (flex; stack on narrow viewports per existing dashboard responsive conventions).

- **Always fall through** — populated from `always_fall_through`. Caption: "These tools fall through to the default verdict: `require-approval`."
- **May fall through (depending on arguments)** — populated from `may_fall_through`. Caption: "These tools may fall through to the default verdict: `require-approval`, depending on call arguments."

Both boxes always render their heading and caption. Empty boxes show a muted "none" placeholder so the layout stays symmetric.

**3. CSS.** Add styles needed by the new structure:

- `.rule-headline` — sentence-style flex row with verdict word, tool glob (mono), `with` suffix.
- `.rule-pattern` — indented, monospace, one per line under the headline.
- `.fallthrough-row` — flex container, gap, wraps to stacked on narrow.
- `.fallthrough-box` — bordered panel matching `.rule-card` look, min-width on flex.
- `.fallthrough-empty` — muted "none" placeholder.

Keep colors and font choices consistent with the current dashboard palette (already defined in `:root`).

**4. JS — `loadRules` update** (current implementation: index.html:1604–1676):

- Read `data.always_fall_through` and `data.may_fall_through` (instead of `data.unmatched`).
- Render the headline string by joining `rule.verdict + " " + rule.tool` and appending ` with` only when `rule.args && rule.args.length`. Then render each arg as a child line; for each, parse `arg.match` (it arrives as already-parsed JSON since the response is JSON-decoded) and pick `=` or `~`. Wrap the value in `"…"`.
- HTML-escape everything user-controlled (verdict, tool glob, path, match value) via the existing `esc` helper. Regex sources can contain anything; do not bypass the escape.
- Render both fall-through boxes; show "none" placeholder when empty.

**Acceptance Criteria:**

- A no-args rule renders a one-line headline with no `with` suffix and no pattern lines. Visible in a browser by running `make build && ./mcp-broker serve` against a config with one no-args rule.
- A with-args rule renders headline + `with` + one indented pattern line per arg, using `=` for strings and `~` for regex.
- The two fall-through boxes appear side-by-side on desktop viewports and stack on narrow viewports. Empty boxes show "none."
- All existing dashboard test assertions still pass (the HTML rewrite does not break tests, which only assert the JSON API).
- Manual UI check: load the dashboard, switch to the rules tab, confirm verdict colors are correct, paths/values are HTML-escaped (try a path with `<`), and the rules-tab layout matches the design's text mockups.

**Notes:**

- `match` arrives in the JSON response as the decoded form of `json.RawMessage` — when Go re-encodes `ArgPattern.Match`, the `RawMessage` is written verbatim. So the browser receives a JSON value (string or object), not a base64-encoded blob. Inspect `typeof match === "string"` vs `match && match.regex` to discriminate.
- Do not auto-anchor the rendered regex; show exactly what the author wrote.
- This task does not change any Go file; it is purely the embedded HTML.
- If you cannot manually run the dashboard in this environment, say so explicitly in the summary — type-checks and tests verify code correctness, not feature correctness.

**Commit:** `feat(mcp-broker): render arg patterns and split fall-through`

---

### Task 6: Documentation

**Files:**

- Modify: `mcp-broker/DESIGN.md` (Rules engine section — § "Components" → "Rules engine")
- Modify: `mcp-broker/README.md` (Rules section — under `## Configuration` → `### Rules`)

**What to do:**

**1. `mcp-broker/DESIGN.md`, the "Rules engine" section (currently a single paragraph at line 82):** expand to document optional argument matching. Cover:

- Optional `args` field on `RuleConfig`; AND semantics across patterns.
- Path syntax: dotted segments, integer segments for array indices, no wildcards in v1.
- Matchers: bare string (exact) and `{"regex": "<RE2>"}`. Author-controlled anchoring — explicitly call out the unanchored-substring footgun and the rationale (don't surprise authors who know regex).
- Resolution failures (missing key, type mismatch, out-of-range index) cause the pattern → rule to fail and evaluation continues.
- Value stringification: `encoding/json.Marshal`, plain strings unquoted; `42` → `"42"`, `null` → `"null"`, etc.
- Validation timing: paths and regexes are compiled at engine construction (`rules.New`); errors surface there, not at evaluation.
- Default verdict, fail-closed default, first-match-wins: unchanged.
- Worked example: a `git_push` rule with `remote=origin` AND `commit.message` matching `^feat:`.

The CLAUDE.md doc-purposes note is explicit: DESIGN.md is the spec. Make sure the spec, not just the README, reflects the new behavior.

**2. `mcp-broker/README.md`, the `### Rules` section (line 144):** add an arg-matching subsection with:

- One-paragraph summary.
- A small JSON example showing an `allow` rule constrained on `remote` and a `deny` rule for a regex match.
- A one-line note about the unanchored-regex footgun: "regexes are not auto-anchored — use `^...$` for full-match semantics."

Keep the existing top-level table (`allow`/`deny`/`require-approval`) and the default-verdict line — they are unchanged.

**Acceptance Criteria:**

- DESIGN.md "Rules engine" section documents path syntax, both matchers, AND semantics, fail-closed-on-resolution-failure, eager validation, and the regex anchoring footgun. Worked example present.
- README.md "Rules" section gains a subsection (or paragraph + example) showing `args` usage with both an exact and a regex matcher, plus the anchoring footgun call-out.
- Markdown lints clean (no broken tables, no extraneous code-fence languages). Verify via `git diff` review.

**Notes:**

- Do not duplicate prose between DESIGN.md and README.md — DESIGN.md is the spec, README.md is the user-facing summary. Per CLAUDE.md (root): "Each doc has a distinct audience and scope — don't duplicate content between them."
- Do not touch `mcp-broker/CLAUDE.md` for this feature unless a new gotcha emerges (e.g., a non-obvious invariant). The feature is mostly self-evident from the code and DESIGN.md.

**Commit:** `docs(mcp-broker): document rule argument matching`

---
