# Issues, issue comments, labels, and assignees via the GitHub REST API.
# Docs: https://docs.github.com/en/rest/issues
#
# Uses the `github_token` secret and the same setup as 10-github-pulls.hcl --
# see that file for fine-grained token creation and `secret add` steps.
# Fine-grained permission required: `Issues: Read and write` (plus the
# base Metadata/Contents reads).
#
# Without that secret bound, requests matched here fail with HTTP 403 and
# header `X-Agent-Gateway-Reason: secret-unresolved`.
#
# ---- PR comments share this path ------------------------------------------
#
# `POST /repos/O/R/issues/N/comments` creates a comment on an issue OR a
# top-level conversation comment on a pull request -- in GitHub's data
# model PRs are issues. An agent posting a top-level comment on a PR
# (`gh pr comment`) hits THIS rule, not the pulls-file review-comment
# rule.
#
# ---- PATCH is polymorphic -------------------------------------------------
#
# `PATCH /repos/O/R/issues/N` edits title/body/labels/assignees AND also
# closes (`state: closed`) or reopens (`state: open`) the issue. The
# approver must inspect the request body to tell an edit from a
# close/reopen. There is no REST DELETE for issues -- deletion is
# GraphQL-only, gated separately by 10-github-graphql.hcl.
#
# ---- Cross-repo list endpoints (included as reads) -------------------------
#
# Read endpoints NOT under /repos/O/R/ cover your own and org-wide issue
# views:
#
#   GET /issues                -- issues across every repo you can access
#   GET /user/issues           -- issues assigned to the authenticated user
#   GET /orgs/ORG/issues       -- issues across repos in ORG
#
# ---- Deliberately NOT included --------------------------------------------
#
#   DELETE /repos/O/R/issues/comments/ID -- removes comments from the
#                                           public history; rarely what
#                                           an agent should do. Add a
#                                           rule if needed.
#   DELETE /repos/O/R/labels/NAME        -- removes the label from EVERY
#                                           issue in the repo. Admin op.
#   POST / PATCH /repos/O/R/labels       -- creating or editing the
#                                           repo's label set is admin-
#                                           adjacent; add manually if an
#                                           agent needs it.
# ---------------------------------------------------------------------------

rule "github-list-issues-repo" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/issues"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-get-issue" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/issues/**"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-list-labels" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/labels"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-get-label" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/labels/*"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-list-assignees" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/assignees"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-check-assignable" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/assignees/*"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-list-cross-repo-issues" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/issues"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-list-user-issues" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/user/issues"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-list-org-issues" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/orgs/*/issues"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-create-issue" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/issues"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-edit-issue" {
  match {
    host   = "api.github.com"
    method = "PATCH"
    path   = "/repos/*/*/issues/*"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-create-issue-comment" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/issues/*/comments"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-edit-issue-comment" {
  match {
    host   = "api.github.com"
    method = "PATCH"
    path   = "/repos/*/*/issues/comments/*"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-add-labels" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/issues/*/labels"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-replace-labels" {
  match {
    host   = "api.github.com"
    method = "PUT"
    path   = "/repos/*/*/issues/*/labels"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-remove-label" {
  match {
    host   = "api.github.com"
    method = "DELETE"
    path   = "/repos/*/*/issues/*/labels/*"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-add-assignees" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/issues/*/assignees"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-remove-assignees" {
  match {
    host   = "api.github.com"
    method = "DELETE"
    path   = "/repos/*/*/issues/*/assignees"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-lock-issue" {
  match {
    host   = "api.github.com"
    method = "PUT"
    path   = "/repos/*/*/issues/*/lock"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-unlock-issue" {
  match {
    host   = "api.github.com"
    method = "DELETE"
    path   = "/repos/*/*/issues/*/lock"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}
