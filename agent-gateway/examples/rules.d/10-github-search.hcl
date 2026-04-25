# GitHub search -- code, issues, PRs, commits, repos, users, topics, labels.
# Docs: https://docs.github.com/en/rest/search
#
# Uses the `github_token` secret and the same setup as 10-github-pulls.hcl --
# see that file for fine-grained token creation and `secret add` steps.
#
# Without this, requests matched by these rules will fail with HTTP 403 and
# header `X-Agent-Gateway-Reason: secret-unresolved` — the rule matched but
# the gateway had no `${secrets.github_token}` to inject.
#
# All search endpoints are GET-only and read-only; one `allow` rule covers
# the whole surface.
#
# ---- Rate limits are separate from the core API ---------------------------
#
# Search has its own rate budget: 30 requests per minute authenticated,
# 10 per minute unauthenticated. `GET /search/code` is stricter at 10 per
# minute authenticated. 429s come with a `Retry-After` header; agents
# that burst search queries will hit this fast.
#
# ---- `GET /search/code` note ----------------------------------------------
#
# `search/code` queries return snippets from every repo the token can
# see, across entire orgs if the token is broadly scoped. If the PAT you
# bound here is scoped narrowly (single repo / single org), this is no
# concern; if it's broadly scoped and you care about exfil, pull this
# endpoint into its own rule and flip it to `require-approval`:
#
#   rule "github-search-code-gated" {
#     match {
#       host   = "api.github.com"
#       method = "GET"
#       path   = "/search/code"
#     }
#     verdict = "require-approval"
#     inject {
#       replace_header = {
#         "Authorization" = "Bearer ${secrets.github_token}"
#       }
#     }
#   }
#
# Place it above the blanket `github-search` rule (first-match-wins).
#
# ---- Endpoints covered ----------------------------------------------------
#
#   GET /search/code          -- code search (supports text-match Accept)
#   GET /search/commits       -- commit search
#   GET /search/issues        -- issues AND PRs (use `is:pr` / `is:issue`)
#   GET /search/labels        -- labels within a repo
#   GET /search/repositories  -- repo search
#   GET /search/topics        -- topic search
#   GET /search/users         -- user/org search
# ---------------------------------------------------------------------------

rule "github-search" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/search/*"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}
