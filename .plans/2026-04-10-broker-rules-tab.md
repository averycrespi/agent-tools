# Broker Rules Tab Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Add a read-only "Rules" tab to the mcp-broker dashboard (between Tools and Audit Log) that shows configured rules with their matching discovered tools, so users can debug why a tool call got a particular verdict.

**Architecture:** Extend `*rules.Engine` with two new methods (`Rules()` and `EvaluateWithRule()`), add a `RulesLister` interface to `internal/dashboard`, wire the engine into `dashboard.New`, add a `GET /api/rules` handler that cross-references rules against discovered tools server-side, and add the tab HTML/CSS/JS to the embedded `index.html`.

**Tech Stack:** Go 1.22+, `net/http`, `path/filepath.Match`, vanilla JS, `stretchr/testify/require`, `go test -race`.

**Design doc:** `.designs/2026-04-10-broker-rules-tab.md`

---

## Context for the implementing engineer

### Key files you'll be touching

- `mcp-broker/internal/rules/rules.go` — glob-matching engine, first-match-wins. Currently has one public method, `Evaluate(tool) Verdict`.
- `mcp-broker/internal/rules/rules_test.go` — table-flavor unit tests using `stretchr/testify/require`.
- `mcp-broker/internal/config/config.go` — defines `RuleConfig{Tool, Verdict string}`. Do not modify.
- `mcp-broker/internal/dashboard/dashboard.go` — constructor `New(tools ToolLister, auditor AuditQuerier, logger *slog.Logger) *Dashboard` and HTTP handler registration. You will add a new parameter and a new handler.
- `mcp-broker/internal/dashboard/dashboard_test.go` — existing tests call `New(nil, nil, nil)`. These **must** be updated to the new 4-arg signature as part of task 3.
- `mcp-broker/internal/dashboard/index.html` — 1221-line embedded dashboard. Tab bar is at line 674, tab content divs start at 683, `switchTab` dispatch at 737, verdict color utility classes `.verdict-allow` / `.verdict-deny` / `.verdict-require-approval` are at lines 605–615.
- `mcp-broker/internal/server/manager.go` — `server.Tool` type has `Name`, `Description`, `InputSchema` fields. `ToolLister.Tools()` returns `[]server.Tool`.
- `mcp-broker/cmd/mcp-broker/serve.go:109` — call site `dash := dashboard.New(mgr, auditor, logger.With("component", "dashboard"))`. Update to pass the `*rules.Engine`.
- `mcp-broker/DESIGN.md:121-128` — lists the dashboard tabs. Update for the new tab.

### Project conventions (from `mcp-broker/CLAUDE.md`)

- Wrap errors: `fmt.Errorf("doing X: %w", err)`.
- Logger is nil-checked in dashboard package — follow the existing pattern (`if d.logger != nil { d.logger.Error(...) }`).
- Run `make audit` (tidy + fmt + lint + test + govulncheck) before committing. From the repo root, `cd mcp-broker && make audit` also works.
- Conventional commits: `<type>(<scope>): <description>`, imperative mood, under 50 chars, no trailing period. Types: feat, fix, chore, docs, refactor, test.

### TDD discipline

Every task follows test-first: write the failing test, run it and see it fail for the right reason, write the minimal code to pass, re-run to see green, commit. Do not batch multiple tasks into one commit.

---

## Task 1: Add `Engine.Rules()` method

**Files:**

- Modify: `mcp-broker/internal/rules/rules.go`
- Test: `mcp-broker/internal/rules/rules_test.go`

**Step 1: Write the failing test**

Append to `mcp-broker/internal/rules/rules_test.go`:

```go
func TestEngine_Rules_ReturnsConfiguredRules(t *testing.T) {
	input := []config.RuleConfig{
		{Tool: "github.*", Verdict: "allow"},
		{Tool: "*", Verdict: "deny"},
	}
	e := New(input)
	require.Equal(t, input, e.Rules())
}

func TestEngine_Rules_EmptyWhenNil(t *testing.T) {
	e := New(nil)
	require.Empty(t, e.Rules())
}
```

**Step 2: Run the test and verify it fails**

```bash
cd mcp-broker && go test -race ./internal/rules/ -run TestEngine_Rules -v
```

Expected: FAIL — `e.Rules undefined (type *Engine has no field or method Rules)`.

**Step 3: Implement `Rules()`**

Add this method to `mcp-broker/internal/rules/rules.go`, directly below `New` (around line 54):

```go
// Rules returns the configured rules in evaluation order.
func (e *Engine) Rules() []config.RuleConfig {
	return e.rules
}
```

**Step 4: Run tests and verify they pass**

```bash
cd mcp-broker && go test -race ./internal/rules/ -v
```

Expected: all rules package tests pass, including the two new ones.

**Step 5: Commit**

```bash
git add mcp-broker/internal/rules/rules.go mcp-broker/internal/rules/rules_test.go
git commit -m "feat(rules): expose configured rules via Rules method"
```

---

## Task 2: Add `Engine.EvaluateWithRule()` and refactor `Evaluate`

**Rationale:** The dashboard handler needs to know _which_ rule index matched a tool, not just the verdict. We add `EvaluateWithRule(tool) (Verdict, int)` as the single source of truth and rewrite `Evaluate` to delegate, so we never maintain two glob-matching loops.

**Files:**

- Modify: `mcp-broker/internal/rules/rules.go`
- Test: `mcp-broker/internal/rules/rules_test.go`

**Step 1: Write the failing test**

Append to `mcp-broker/internal/rules/rules_test.go`:

```go
func TestEngine_EvaluateWithRule_FirstMatchWins(t *testing.T) {
	e := New([]config.RuleConfig{
		{Tool: "github.push", Verdict: "require-approval"}, // index 0
		{Tool: "github.*", Verdict: "allow"},               // index 1
		{Tool: "*", Verdict: "deny"},                       // index 2
	})

	v, idx := e.EvaluateWithRule("github.push")
	require.Equal(t, RequireApproval, v)
	require.Equal(t, 0, idx)

	v, idx = e.EvaluateWithRule("github.get_pr")
	require.Equal(t, Allow, v)
	require.Equal(t, 1, idx)

	v, idx = e.EvaluateWithRule("linear.search")
	require.Equal(t, Deny, v)
	require.Equal(t, 2, idx)
}

func TestEngine_EvaluateWithRule_NoMatchReturnsNegativeOne(t *testing.T) {
	e := New([]config.RuleConfig{
		{Tool: "github.*", Verdict: "allow"},
	})
	v, idx := e.EvaluateWithRule("linear.search")
	require.Equal(t, RequireApproval, v) // default
	require.Equal(t, -1, idx)
}

func TestEngine_EvaluateWithRule_EmptyRules(t *testing.T) {
	e := New(nil)
	v, idx := e.EvaluateWithRule("anything")
	require.Equal(t, RequireApproval, v)
	require.Equal(t, -1, idx)
}
```

**Step 2: Run the test and verify it fails**

```bash
cd mcp-broker && go test -race ./internal/rules/ -run TestEngine_EvaluateWithRule -v
```

Expected: FAIL — `e.EvaluateWithRule undefined`.

**Step 3: Implement `EvaluateWithRule` and delegate `Evaluate`**

Replace the existing `Evaluate` method in `mcp-broker/internal/rules/rules.go` (lines 55–68) with:

```go
// Evaluate returns the verdict for the given tool name.
// First matching rule wins. Default is require-approval.
func (e *Engine) Evaluate(tool string) Verdict {
	v, _ := e.EvaluateWithRule(tool)
	return v
}

// EvaluateWithRule returns the verdict and the zero-based index of the rule
// that matched. Returns (RequireApproval, -1) when no rule matches.
func (e *Engine) EvaluateWithRule(tool string) (Verdict, int) {
	for i, rule := range e.rules {
		matched, err := filepath.Match(rule.Tool, tool)
		if err != nil {
			continue
		}
		if matched {
			return ParseVerdict(rule.Verdict), i
		}
	}
	return RequireApproval, -1
}
```

**Step 4: Run the full rules test suite and verify everything passes**

```bash
cd mcp-broker && go test -race ./internal/rules/ -v
```

Expected: all tests pass. The existing `TestEngine_Evaluate_*` tests must still pass — they now exercise `Evaluate` → `EvaluateWithRule` delegation.

**Step 5: Commit**

```bash
git add mcp-broker/internal/rules/rules.go mcp-broker/internal/rules/rules_test.go
git commit -m "feat(rules): add EvaluateWithRule returning matched index"
```

---

## Task 3: Update dashboard constructor to accept `RulesLister`

**Rationale:** Before we can add a handler that reads rules, the `Dashboard` struct needs access to a `RulesLister`. This task is a pure plumbing change — no new behavior, but it lights up the signature so task 4 can add the handler.

**Files:**

- Modify: `mcp-broker/internal/dashboard/dashboard.go`
- Modify: `mcp-broker/internal/dashboard/dashboard_test.go`
- Modify: `mcp-broker/cmd/mcp-broker/serve.go:109`

**Step 1: Update the dashboard test call sites to the new signature**

All five calls to `dashboard.New(nil, nil, nil)` in `mcp-broker/internal/dashboard/dashboard_test.go` need to become `dashboard.New(nil, nil, nil, nil)`. These are at lines 17, 55, 92, 116, 146 in the current file.

**Step 2: Run the dashboard tests to watch them fail**

```bash
cd mcp-broker && go test -race ./internal/dashboard/ -v
```

Expected: compile error — `not enough arguments in call to New` (since we haven't changed the signature yet). This is the "failing test" — the compiler is telling us the test file now demands a 4-arg `New`.

**Step 3: Implement the signature change**

In `mcp-broker/internal/dashboard/dashboard.go`:

1. Import `config`:

```go
import (
	// ... existing imports ...
	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)
```

2. Add the `RulesLister` interface directly below `ToolLister` (around line 42):

```go
// RulesLister provides the configured policy rules in evaluation order.
type RulesLister interface {
	Rules() []config.RuleConfig
}
```

3. Add a `rules` field to the `Dashboard` struct (around line 51):

```go
type Dashboard struct {
	mu      sync.Mutex
	pending map[string]*pendingRequest
	decided []decidedRequest
	clients []chan []byte
	tools   ToolLister
	rules   RulesLister
	auditor AuditQuerier
	logger  *slog.Logger
}
```

4. Update `New` to accept and store it (around line 62):

```go
// New creates a Dashboard.
func New(tools ToolLister, rules RulesLister, auditor AuditQuerier, logger *slog.Logger) *Dashboard {
	return &Dashboard{
		pending: make(map[string]*pendingRequest),
		tools:   tools,
		rules:   rules,
		auditor: auditor,
		logger:  logger,
	}
}
```

**Step 4: Update the production call site in `serve.go`**

In `mcp-broker/cmd/mcp-broker/serve.go:109`, change:

```go
dash := dashboard.New(mgr, auditor, logger.With("component", "dashboard"))
```

to:

```go
dash := dashboard.New(mgr, engine, auditor, logger.With("component", "dashboard"))
```

The variable `engine` is the `*rules.Engine` already constructed earlier in `serve.go`. If it happens to be named differently in the file, use whatever variable holds the `*rules.Engine` — check with `grep -n "rules.New" mcp-broker/cmd/mcp-broker/serve.go` before editing. It must be passed to `dashboard.New` as the second argument.

**Step 5: Verify everything compiles and tests pass**

```bash
cd mcp-broker && go build ./... && go test -race ./internal/dashboard/ ./cmd/... -v
```

Expected: build succeeds, all dashboard tests pass. No new tests yet — this task is pure plumbing.

**Step 6: Commit**

```bash
git add mcp-broker/internal/dashboard/dashboard.go \
        mcp-broker/internal/dashboard/dashboard_test.go \
        mcp-broker/cmd/mcp-broker/serve.go
git commit -m "refactor(dashboard): accept RulesLister in constructor"
```

---

## Task 4: Implement `GET /api/rules` handler

**Rationale:** The handler is the heart of the feature — it returns rules joined with the tools that match them. Matching is computed server-side by calling `engine.EvaluateWithRule(tool.Name)` for each discovered tool, guaranteeing the dashboard never drifts from the real engine semantics.

**Files:**

- Modify: `mcp-broker/internal/dashboard/dashboard.go`
- Test: `mcp-broker/internal/dashboard/dashboard_test.go`

**Step 1: Write the failing test**

First, add fake implementations of `ToolLister` and `RulesLister` at the top of `mcp-broker/internal/dashboard/dashboard_test.go` (after the imports):

```go
type fakeToolLister struct{ tools []server.Tool }

func (f *fakeToolLister) Tools() []server.Tool { return f.tools }

type fakeRulesLister struct{ rules []config.RuleConfig }

func (f *fakeRulesLister) Rules() []config.RuleConfig { return f.rules }
```

Add the needed imports to `dashboard_test.go`:

```go
import (
	// ... existing ...
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)
```

Then append these tests:

```go
func TestHandleRules_GroupsToolsByMatchingRule(t *testing.T) {
	tools := &fakeToolLister{tools: []server.Tool{
		{Name: "github.list_prs"},
		{Name: "github.view_pr"},
		{Name: "github.delete_repo"},
		{Name: "fs.write"},
	}}
	rules := &fakeRulesLister{rules: []config.RuleConfig{
		{Tool: "github.delete_*", Verdict: "deny"},
		{Tool: "github.*", Verdict: "allow"},
		{Tool: "*", Verdict: "require-approval"},
	}}
	d := New(tools, rules, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Rules []struct {
			Index   int      `json:"index"`
			Tool    string   `json:"tool"`
			Verdict string   `json:"verdict"`
			Matches []string `json:"matches"`
		} `json:"rules"`
		Unmatched       []string `json:"unmatched"`
		DefaultVerdict  string   `json:"default_verdict"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Len(t, body.Rules, 3)

	require.Equal(t, 0, body.Rules[0].Index)
	require.Equal(t, "github.delete_*", body.Rules[0].Tool)
	require.Equal(t, "deny", body.Rules[0].Verdict)
	require.Equal(t, []string{"github.delete_repo"}, body.Rules[0].Matches)

	require.Equal(t, 1, body.Rules[1].Index)
	require.Equal(t, "github.*", body.Rules[1].Tool)
	require.Equal(t, "allow", body.Rules[1].Verdict)
	require.ElementsMatch(t, []string{"github.list_prs", "github.view_pr"}, body.Rules[1].Matches)

	require.Equal(t, 2, body.Rules[2].Index)
	require.Equal(t, "*", body.Rules[2].Tool)
	require.Equal(t, "require-approval", body.Rules[2].Verdict)
	require.Equal(t, []string{"fs.write"}, body.Rules[2].Matches)

	require.Empty(t, body.Unmatched)
	require.Equal(t, "require-approval", body.DefaultVerdict)
}

func TestHandleRules_EmptyRules(t *testing.T) {
	tools := &fakeToolLister{tools: []server.Tool{{Name: "fs.write"}}}
	rules := &fakeRulesLister{rules: nil}
	d := New(tools, rules, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Rules          []any    `json:"rules"`
		Unmatched      []string `json:"unmatched"`
		DefaultVerdict string   `json:"default_verdict"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Empty(t, body.Rules)
	require.Equal(t, []string{"fs.write"}, body.Unmatched)
	require.Equal(t, "require-approval", body.DefaultVerdict)
}

func TestHandleRules_RuleWithNoMatches(t *testing.T) {
	tools := &fakeToolLister{tools: []server.Tool{{Name: "fs.write"}}}
	rules := &fakeRulesLister{rules: []config.RuleConfig{
		{Tool: "github.*", Verdict: "allow"},
		{Tool: "*", Verdict: "require-approval"},
	}}
	d := New(tools, rules, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Rules []struct {
			Matches []string `json:"matches"`
		} `json:"rules"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Len(t, body.Rules, 2)
	require.Empty(t, body.Rules[0].Matches) // github.* has no matches
	require.Equal(t, []string{"fs.write"}, body.Rules[1].Matches)
}

func TestHandleRules_NilLister(t *testing.T) {
	d := New(nil, nil, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Rules          []any    `json:"rules"`
		Unmatched      []string `json:"unmatched"`
		DefaultVerdict string   `json:"default_verdict"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Empty(t, body.Rules)
	require.Empty(t, body.Unmatched)
	require.Equal(t, "require-approval", body.DefaultVerdict)
}
```

**Step 2: Run the tests and verify they fail**

```bash
cd mcp-broker && go test -race ./internal/dashboard/ -run TestHandleRules -v
```

Expected: FAIL — `404 Not Found` from `httptest` (the `/api/rules` route doesn't exist yet).

**Step 3: Implement the handler**

In `mcp-broker/internal/dashboard/dashboard.go`, add `path/filepath` to the imports if not already present. Register the new route inside `Handler()` (around line 78, next to the other `mux.HandleFunc` calls):

```go
mux.HandleFunc("GET /api/rules", d.handleRules)
```

Add the handler method near `handleTools` (around line 187):

```go
func (d *Dashboard) handleRules(w http.ResponseWriter, _ *http.Request) {
	type ruleView struct {
		Index   int      `json:"index"`
		Tool    string   `json:"tool"`
		Verdict string   `json:"verdict"`
		Matches []string `json:"matches"`
	}

	var rules []config.RuleConfig
	if d.rules != nil {
		rules = d.rules.Rules()
	}

	var tools []server.Tool
	if d.tools != nil {
		tools = d.tools.Tools()
	}

	views := make([]ruleView, len(rules))
	for i, r := range rules {
		views[i] = ruleView{
			Index:   i,
			Tool:    r.Tool,
			Verdict: r.Verdict,
			Matches: []string{},
		}
	}

	unmatched := []string{}
	for _, tool := range tools {
		idx := -1
		for i, r := range rules {
			matched, err := filepath.Match(r.Tool, tool.Name)
			if err != nil {
				continue
			}
			if matched {
				idx = i
				break
			}
		}
		if idx >= 0 {
			views[idx].Matches = append(views[idx].Matches, tool.Name)
		} else {
			unmatched = append(unmatched, tool.Name)
		}
	}

	sort.Strings(unmatched)
	for i := range views {
		sort.Strings(views[i].Matches)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"rules":           views,
		"unmatched":       unmatched,
		"default_verdict": "require-approval",
	})
}
```

**Note on duplication:** The handler re-implements the glob loop rather than calling `engine.EvaluateWithRule` because the dashboard package only sees a `RulesLister` interface, not a full `*rules.Engine`. Keeping the interface narrow is the right call — adding an `EvaluateWithRule` method to the interface would couple the dashboard to another moving part. The loop is five lines and uses the same `filepath.Match` semantics, so drift risk is minimal. If future work introduces more matching complexity, revisit this decision.

**Step 4: Run the dashboard tests and verify they pass**

```bash
cd mcp-broker && go test -race ./internal/dashboard/ -v
```

Expected: all dashboard tests pass, including all four new `TestHandleRules_*` tests.

**Step 5: Commit**

```bash
git add mcp-broker/internal/dashboard/dashboard.go mcp-broker/internal/dashboard/dashboard_test.go
git commit -m "feat(dashboard): add /api/rules endpoint"
```

---

## Task 5: Add the Rules tab to `index.html`

**Rationale:** Final piece — the actual UI. We add the tab button, a content div, CSS (reusing existing tokens), and a `loadRules()` JS function that fetches from `/api/rules` and renders.

**Files:**

- Modify: `mcp-broker/internal/dashboard/index.html`

**Step 1: Add the tab button**

Find line 679–680 in `index.html`:

```html
<button class="tab" onclick="switchTab('tools')" id="tab-tools">Tools</button>
<button class="tab" onclick="switchTab('audit')" id="tab-audit">
  Audit Log
</button>
```

Insert a new button between them:

```html
<button class="tab" onclick="switchTab('tools')" id="tab-tools">Tools</button>
<button class="tab" onclick="switchTab('rules')" id="tab-rules">Rules</button>
<button class="tab" onclick="switchTab('audit')" id="tab-audit">
  Audit Log
</button>
```

**Step 2: Add the content div**

Find the end of the Tools tab content div (around line 700) and the start of the Audit tab (around line 702). Insert a new `#content-rules` div between them:

```html
<!-- Rules Tab -->
<div class="tab-content" id="content-rules">
  <div class="section-title">Rules (evaluated first-match-wins)</div>
  <div id="rules-list"></div>
  <div id="rules-unmatched-section" style="display:none;">
    <div class="section-title" style="margin-top:1.5rem;">Unmatched tools</div>
    <div id="rules-unmatched-desc" class="rules-unmatched-desc"></div>
    <div id="rules-unmatched-list"></div>
  </div>
  <div id="rules-empty" class="empty-state" style="display:none;">
    No rules configured
  </div>
</div>
```

**Step 3: Add the CSS**

Find the end of the `/* Tools tab */` CSS block (around line 435, just before `/* Audit tab */`). Insert the following CSS block:

```css
/* Rules tab */
.rule-card {
  background: var(--bg-panel);
  border: 1px solid var(--border);
  border-radius: 8px;
  margin-bottom: 0.75rem;
  overflow: hidden;
}

.rule-header {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.75rem 1rem;
  border-bottom: 1px solid var(--border);
}

.rule-index {
  font-family: var(--font-mono);
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--text-secondary);
  min-width: 2rem;
}

.rule-tool {
  font-family: var(--font-mono);
  font-size: 0.875rem;
  color: var(--text-primary);
  flex: 1;
}

.rule-verdict {
  font-family: var(--font-mono);
  font-size: 0.75rem;
  font-weight: 600;
}

.rule-match-count {
  font-family: var(--font-mono);
  font-size: 0.6875rem;
  font-weight: 600;
  background: var(--bg-base);
  color: var(--text-secondary);
  padding: 0.1rem 0.5rem;
  border-radius: 9999px;
  border: 1px solid var(--border);
}

.rule-matches {
  padding: 0.5rem 0;
}

.rule-match-item {
  padding: 0.375rem 1rem 0.375rem 2.5rem;
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  color: var(--text-primary);
}

.rule-matches-empty {
  padding: 0.5rem 1rem 0.5rem 2.5rem;
  font-size: 0.8125rem;
  color: var(--text-secondary);
  font-style: italic;
}

.rules-unmatched-desc {
  font-size: 0.8125rem;
  color: var(--text-secondary);
  margin-bottom: 0.5rem;
}
```

**Step 4: Add the `loadRules` JavaScript**

Find the `switchTab` function (line 737) and add rules dispatch:

```js
function switchTab(name) {
  document.querySelectorAll(".tab").forEach(function (t) {
    t.classList.remove("active");
  });
  document.querySelectorAll(".tab-content").forEach(function (c) {
    c.classList.remove("active");
  });
  document.getElementById("tab-" + name).classList.add("active");
  document.getElementById("content-" + name).classList.add("active");

  if (name === "tools") loadTools();
  if (name === "rules") loadRules();
  if (name === "audit") {
    auditOffset = 0;
    loadAudit();
  }
}
```

Then find the `// --- Tools ---` section (around line 1013) and, directly after the `toggleProvider` function (ends around line 1062), insert a new `// --- Rules ---` section:

```js
// --- Rules ---
function loadRules() {
  var list = document.getElementById("rules-list");
  var emptyEl = document.getElementById("rules-empty");
  var unmatchedSection = document.getElementById("rules-unmatched-section");
  var unmatchedDesc = document.getElementById("rules-unmatched-desc");
  var unmatchedList = document.getElementById("rules-unmatched-list");

  fetch("api/rules")
    .then(function (r) {
      return r.json();
    })
    .then(function (data) {
      var rules = data.rules || [];
      var unmatched = data.unmatched || [];
      var defaultVerdict = data.default_verdict || "require-approval";

      if (rules.length === 0) {
        list.innerHTML = "";
        emptyEl.style.display = "block";
      } else {
        emptyEl.style.display = "none";
        var html = "";
        rules.forEach(function (rule) {
          var verdictClass = "verdict-" + rule.verdict;
          html += '<div class="rule-card">';
          html += '<div class="rule-header">';
          html += '<span class="rule-index">#' + (rule.index + 1) + "</span>";
          html += '<span class="rule-tool">' + esc(rule.tool) + "</span>";
          html +=
            '<span class="rule-verdict ' +
            verdictClass +
            '">' +
            esc(rule.verdict) +
            "</span>";
          html +=
            '<span class="rule-match-count">' + rule.matches.length + "</span>";
          html += "</div>";
          html += '<div class="rule-matches">';
          if (rule.matches.length === 0) {
            html += '<div class="rule-matches-empty">no matching tools</div>';
          } else {
            rule.matches.forEach(function (tool) {
              html += '<div class="rule-match-item">' + esc(tool) + "</div>";
            });
          }
          html += "</div>";
          html += "</div>";
        });
        list.innerHTML = html;
      }

      if (unmatched.length === 0) {
        unmatchedSection.style.display = "none";
      } else {
        unmatchedSection.style.display = "block";
        unmatchedDesc.textContent =
          "These tools fall through to the default verdict: " + defaultVerdict;
        var uhtml = "";
        unmatched.forEach(function (tool) {
          uhtml += '<div class="rule-match-item">' + esc(tool) + "</div>";
        });
        unmatchedList.innerHTML = uhtml;
      }
    });
}
```

**Step 5: Build and smoke-test the UI**

```bash
cd mcp-broker && make build
```

Expected: clean build. Then run the broker against your existing config:

```bash
./mcp-broker serve
```

Open the dashboard URL printed to stderr, click the **Rules** tab. You should see:

- Your configured rules, numbered starting at `#1`
- Each rule showing its glob, verdict pill (with existing green/red/amber colors), and match count
- Matching tool names listed under each rule (if any)
- "no matching tools" grey italic text for rules with zero matches
- No "Unmatched tools" section if your config has a catchall `*`

Stop the broker (Ctrl-C) before committing.

**Step 6: Commit**

```bash
git add mcp-broker/internal/dashboard/index.html
git commit -m "feat(dashboard): add rules tab to web UI"
```

---

## Task 6: Update `DESIGN.md` tab list

**Files:**

- Modify: `mcp-broker/DESIGN.md:121-128`

**Step 1: Make the edit**

Current text at `mcp-broker/DESIGN.md:121-128`:

```
### Dashboard (`internal/dashboard`)

- **Approvals tab** — pending requests with approve/deny buttons, decided history
- **Tools tab** — discovered tools grouped by server
- **Audit tab** — paginated audit log with tool filter
```

Replace with:

```
### Dashboard (`internal/dashboard`)

- **Approvals tab** — pending requests with approve/deny buttons, decided history
- **Tools tab** — discovered tools grouped by server
- **Rules tab** — configured rules with the discovered tools matching each (read-only; for debugging verdicts)
- **Audit tab** — paginated audit log with tool filter
```

**Step 2: Commit**

```bash
git add mcp-broker/DESIGN.md
git commit -m "docs: mention rules tab in dashboard section"
```

---

## Task 7: Run `make audit` and fix anything it flags

**Files:** none to edit up front — only if `make audit` surfaces issues.

**Step 1: Run the full audit**

```bash
cd mcp-broker && make audit
```

This runs: `go mod tidy` + `go mod verify` + `goimports -w .` + `golangci-lint run ./...` + `go test -race ./...` + `govulncheck ./...`.

Expected: all green. If lint or govulncheck flags something, fix it and stage the fix. If tests regressed, diagnose — do not `--no-verify` bypass.

**Step 2: If any files changed, commit the fixups**

```bash
git status
# If files changed:
git add <files>
git commit -m "chore: fix lint/format after rules tab"
```

If nothing changed, skip this commit.

---

## Verification checklist (after all tasks)

- [ ] `cd mcp-broker && make audit` passes cleanly
- [ ] Dashboard loads and shows a **Rules** tab between Tools and Audit Log
- [ ] Clicking Rules fetches `/api/rules` and renders ordered rule cards
- [ ] Each rule card shows index (`#1`, `#2`, ...), glob, colored verdict pill, match count chip
- [ ] Rules with zero matches render "no matching tools" in italic grey
- [ ] With the default catchall config, the Unmatched section is hidden
- [ ] Running with a config that removes the catchall shows the Unmatched section
- [ ] Git log shows separate commits for: `feat(rules): expose …`, `feat(rules): add EvaluateWithRule …`, `refactor(dashboard): accept RulesLister …`, `feat(dashboard): add /api/rules …`, `feat(dashboard): add rules tab …`, `docs: …`
