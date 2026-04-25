# GitHub GraphQL -- opaque to path-based matching, gated by default.
# Docs: https://docs.github.com/en/graphql
#
# Uses the `github_token` secret and the same setup as 10-github-pulls.hcl --
# see that file for fine-grained token creation and `secret add` steps.
#
# Without that secret bound, requests matched here fail with HTTP 403 and
# header `X-Agent-Gateway-Reason: secret-unresolved`.
#
# ---- Why this file exists -------------------------------------------------
#
# Every GraphQL request is a `POST /graphql` with a JSON body. The body
# carries the operation (query vs mutation), the selected fields, and the
# variables. The gateway matches on (host, method, path) plus optionally
# headers and body -- but GraphQL makes every request look identical at
# the (host, method, path) level, and the body is a mini-language with
# nested selection sets that path-style rules cannot classify.
#
# That means a single `allow` rule on POST /graphql would silently bypass
# every other write-gating rule in this directory. `gh` and most Octokit-
# based clients can perform arbitrary mutations (create PRs, close
# issues, trigger workflow runs, merge, add collaborators) through
# GraphQL. Leaving GraphQL gated keeps the rest of the rule set honest.
#
# ---- What gets caught -----------------------------------------------------
#
# Most `gh ... --json FIELDS` invocations issue GraphQL under the hood:
#
#   gh pr list --json ...        gh pr view --json ...
#   gh issue list --json ...     gh issue view --json ...
#   gh pr checks --json ...      gh search ... --json ...
#
# Every one will block on dashboard approval with this rule in place. If
# your agent is read-heavy via `gh --json`, you have three options:
#
#   1. Accept the approval prompts. Safest, chattiest.
#   2. Switch the agent to explicit REST invocations
#      (`gh api repos/O/R/pulls/N`). The 10-github-pulls.hcl and friends
#      allow those reads without approval.
#   3. Relax this rule to `allow` -- with full awareness that doing so
#      re-opens the write surface. Only appropriate for agents you
#      already trust to not issue mutations.
#
# ---- Better matching is possible (and not done here) ----------------------
#
# The gateway can body-match with `json_body { jsonpath ... }`, and the
# GraphQL body has a parseable shape: the top-level `query` string starts
# with the keyword `query` or `mutation`. A future refinement could allow
# `query { ... }` bodies and require approval only for `mutation { ... }`
# bodies. That is out of scope for this example file -- the opaque-by-
# default stance is the conservative choice.
# ---------------------------------------------------------------------------

rule "github-graphql" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/graphql"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}
