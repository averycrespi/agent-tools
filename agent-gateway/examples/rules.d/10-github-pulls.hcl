# Pull requests via the GitHub REST API -- reads allowed, writes gated.
# Docs: https://docs.github.com/en/rest/pulls
#
# This file is the "primary" of the 10-github-*.hcl example set; it contains
# the full token setup instructions. The other 10-github-*.hcl files reuse
# the same `github_token` secret and refer back here for setup.
#
# ---- Create a Personal Access Token ---------------------------------------
#
# https://github.com/settings/tokens -- "Fine-grained tokens" (recommended).
# Fine-grained tokens are repo-scoped and have per-permission granularity;
# classic tokens apply org-wide with coarse `repo` / `admin:org`-style
# scopes. Fine-grained narrows blast radius dramatically if the token
# leaks.
#
# Minimum fine-grained permissions for this file:
#   Pull requests:   Read and write
#   Contents:        Read  (required for PR metadata, diff/patch, files list)
#   Metadata:        Read  (always required)
#
# For the full 10-github-*.hcl example set, also add:
#   Issues:          Read and write    (10-github-issues.hcl)
#   Actions:         Read and write    (10-github-actions.hcl -- cache delete, rerun)
#   Commit statuses: Read              (PR check status display)
#
# ---- One-time setup on the host -------------------------------------------
#
# Bind the secret to api.github.com:
#
#   printf '%s' "<your-PAT>" \
#     | agent-gateway secret add github_token --host api.github.com
#
# ---- Smoke test inside the sandbox ----------------------------------------
#
#   export GH_TOKEN=dummy
#   gh pr list --repo owner/repo
#
# The dummy GH_TOKEN is overwritten at the gateway with the real PAT.
# GitHub accepts both `Authorization: token <pat>` and `Authorization:
# Bearer <pat>`; this file injects the Bearer form to match the agent-
# gateway documentation convention.
#
# ---- Redirect hosts: add to config.hcl no_intercept_hosts -----------------
#
# Several GitHub responses 302 to pre-signed short-lived URLs on hosts
# OTHER than api.github.com. Those URLs carry their own auth (signed in
# the query string) and do not need the PAT injected; MITM'ing them only
# breaks the response. Add these to `proxy_behavior.no_intercept_hosts`
# in config.hcl before using the actions or archive-download surface:
#
#   codeload.github.com                           -- archive tarball/zipball
#   objects.githubusercontent.com                 -- release asset downloads
#   productionresultssa*.blob.core.windows.net    -- Actions logs + artifacts
#
# ---- `Accept: application/vnd.github.diff` / `.patch` ---------------------
#
# `GET /repos/O/R/pulls/N` serves JSON by default but returns diff or
# patch bodies when the client sets `Accept: application/vnd.github.diff`
# or `.patch` (this is what `gh pr diff` does). The same is true of
# `GET /repos/O/R/commits/SHA`. Path-based rules treat all three
# identically, which is correct -- the diff/patch responses are still
# reads.
#
# ---- PATCH is polymorphic -------------------------------------------------
#
# `PATCH /repos/O/R/pulls/N` is used for title edit, body edit, base
# change, requested-reviewer updates, AND close/reopen (`state: closed` /
# `state: open`). Path matching can't distinguish; the approver must
# inspect the request body in the dashboard card to tell an edit from a
# close. Same concern applies to `PATCH .../issues/N` in
# 10-github-issues.hcl.
#
# ---- Comment taxonomy -----------------------------------------------------
#
# PRs have TWO kinds of comments:
#
#   * Review comments -- anchored to a line of the diff:
#       POST /repos/O/R/pulls/N/comments        (this file)
#
#   * Issue-style comments -- top-level PR conversation:
#       POST /repos/O/R/issues/N/comments       (10-github-issues.hcl)
#
# An agent posting "LGTM" at the top of a PR needs the issues-file rule;
# an agent leaving feedback on line 42 of a changed file needs this one.
#
# ---- Deliberately NOT included --------------------------------------------
#
#   PUT /repos/O/R/pulls/N/reviews/ID/dismissals -- dismiss someone
#                                                   else's review. Noisy;
#                                                   add if actively needed.
#   DELETE /repos/O/R/git/refs/heads/BRANCH      -- `gh pr merge
#                                                   --delete-branch` runs
#                                                   this after merging.
#                                                   Branch deletion is out
#                                                   of scope (includes
#                                                   main/release branches);
#                                                   let GitHub's
#                                                   auto-delete-head-
#                                                   branches repo setting
#                                                   do it instead.
# ---------------------------------------------------------------------------

rule "github-list-pulls" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/pulls"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-get-pull" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/pulls/**"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-create-pull" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/pulls"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-edit-pull" {
  match {
    host   = "api.github.com"
    method = "PATCH"
    path   = "/repos/*/*/pulls/*"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-merge-pull" {
  match {
    host   = "api.github.com"
    method = "PUT"
    path   = "/repos/*/*/pulls/*/merge"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-update-pull-branch" {
  match {
    host   = "api.github.com"
    method = "PUT"
    path   = "/repos/*/*/pulls/*/update-branch"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-create-review" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/pulls/*/reviews"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-create-review-comment" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/pulls/*/comments"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-request-reviewers" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/pulls/*/requested_reviewers"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}
