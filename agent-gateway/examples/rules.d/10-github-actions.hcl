# GitHub Actions -- workflows, runs, jobs, artifacts, caches.
# Docs: https://docs.github.com/en/rest/actions
#
# Uses the `github_token` secret and the same setup as 10-github-pulls.hcl --
# see that file for fine-grained token creation and `secret add` steps.
# Fine-grained permission required: `Actions: Read and write`.
#
# Without this, requests matched by these rules will fail with HTTP 403 and
# header `X-Agent-Gateway-Reason: secret-unresolved` — the rule matched but
# the gateway had no `${secrets.github_token}` to inject.
#
# ---- Logs & artifacts redirect to Azure Blob ------------------------------
#
# `GET /repos/O/R/actions/runs/ID/logs`, `/actions/jobs/ID/logs`, and
# `/actions/artifacts/ID/ARCHIVE` return a 302 to a short-lived SAS URL
# on `productionresultssa*.blob.core.windows.net`. That URL is pre-signed
# and does not carry the PAT; MITM'ing it only breaks the download. Add:
#
#   productionresultssa*.blob.core.windows.net
#
# to `proxy_behavior.no_intercept_hosts` in config.hcl. Without that,
# `gh run view --log`, `gh run download`, and artifact fetches will stall
# or 4xx at the gateway.
#
# ---- High-risk operations (flag prominently in the approval UI) -----------
#
# `POST /repos/O/R/actions/workflows/WF/dispatches` triggers a workflow
# run with attacker-controlled inputs. If the workflow executes arbitrary
# code -- and almost all CI workflows do -- this is remote code execution
# on the runner, with whatever secrets CI has access to. Treat approvals
# here with matching care.
#
# `POST /repos/O/R/actions/runs/ID/pending_deployments` approves or
# rejects a deployment to a protected environment. This is the production
# gate -- approving it may push code live. Treat with maximum caution.
#
# ---- Deliberately NOT included --------------------------------------------
#
#   POST /repos/O/R/actions/runs/ID/force-cancel -- bypasses cleanup, can
#                                                    leave resources
#                                                    dangling. Denied in
#                                                    00-github-denylist.hcl.
#   DELETE /repos/O/R/actions/runs/ID           -- deletes run history.
#                                                    Denied in
#                                                    00-github-denylist.hcl.
#   DELETE /repos/O/R/actions/runs/ID/logs      -- deletes logs
#                                                    (evidence). Denied
#                                                    in 00-github-denylist.hcl.
#   DELETE /repos/O/R/actions/artifacts/ID      -- deletes an artifact
#                                                    that may be the only
#                                                    copy of test output.
#                                                    Add rule if needed.
# ---------------------------------------------------------------------------

rule "github-list-workflows" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/actions/workflows"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-get-workflow" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/actions/workflows/**"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-list-runs-repo" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/actions/runs"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-get-run" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/actions/runs/**"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-get-job" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/actions/jobs/**"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-list-artifacts" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/actions/artifacts"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-get-artifact" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/actions/artifacts/**"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-list-caches" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/actions/caches"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-cache-usage" {
  match {
    host   = "api.github.com"
    method = "GET"
    path   = "/repos/*/*/actions/cache/usage"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-rerun-workflow" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/actions/runs/*/rerun"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-rerun-failed-jobs" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/actions/runs/*/rerun-failed-jobs"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-cancel-run" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/actions/runs/*/cancel"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-approve-run" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/actions/runs/*/approve"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-approve-deployment" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/actions/runs/*/pending_deployments"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-dispatch-workflow" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/actions/workflows/*/dispatches"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-enable-workflow" {
  match {
    host   = "api.github.com"
    method = "PUT"
    path   = "/repos/*/*/actions/workflows/*/enable"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-disable-workflow" {
  match {
    host   = "api.github.com"
    method = "PUT"
    path   = "/repos/*/*/actions/workflows/*/disable"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-delete-cache-by-id" {
  match {
    host   = "api.github.com"
    method = "DELETE"
    path   = "/repos/*/*/actions/caches/*"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}

rule "github-delete-cache-by-key" {
  match {
    host   = "api.github.com"
    method = "DELETE"
    path   = "/repos/*/*/actions/caches"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.github_token}"
    }
  }
}
