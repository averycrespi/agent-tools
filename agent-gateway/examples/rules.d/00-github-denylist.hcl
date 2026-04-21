# Hard-deny rules for GitHub endpoints that are more likely dangerous than
# useful for a coding agent. Matching requests return 403 to the agent
# without being forwarded, and are audited with `outcome='blocked'`.
#
# Uses no secret and no injection -- deny rules never forward, so there
# is nothing to inject. See 10-github-pulls.hcl for the shared token setup.
#
# ---- Filename prefix (`00-`) ----------------------------------------------
#
# The `00-` prefix loads this file first, ahead of the other
# 10-github-*.hcl files, matching the convention in docs/rules.md for
# deny-first rule sets. No deny path in this file currently overlaps
# with an allow/approval path in the other files -- the deny would fire
# either way -- but loading first future-proofs against accidental
# overlap when editing.
#
# ---- Why deny instead of omit ---------------------------------------------
#
# A request with no matching rule falls through with the sandbox's dummy
# credential and 401s upstream. That has the same "request did not
# happen" outcome as a deny. The reason to write an explicit deny is the
# AUDIT SIGNAL: a `blocked` row in the audit log tells you the agent
# *tried* the operation. "Fell through to 401" looks identical in the
# audit log to "agent asked for something we don't have a rule for yet"
# and so tends to get ignored. "Explicitly denied" is a louder signal.
#
# If a specific endpoint here becomes legitimately necessary, move its
# rule out of this file into an allow-or-approval rule elsewhere. Don't
# flip `deny` to `allow` in place -- the category comments below explain
# WHY each group is blocked, and that context should travel with the rule.
#
# ---- Category 1: Persistent access / backdoor installation ----------------
#
# Webhooks, SSH/deploy keys, GPG/signing keys, collaborator grants, org
# memberships. All install persistent access that outlives this PAT.
# Webhooks in particular are the canonical exfil primitive -- one POST
# and every future push/PR/issue event streams to an attacker URL.
#
# ---- Category 2: Privacy / visibility changes -----------------------------
#
# `PATCH /repos/O/R` can flip `private: false` and take a private repo
# public with a single request; path-based matching can't see the body,
# so the whole endpoint is denied. Repo transfer hands the repo to
# another account entirely.
#
# ---- Category 3: Destructive / tracks-covering ----------------------------
#
# Repo deletion, run record deletion, log deletion, force-cancel (skips
# cleanup). All delete evidence or state in a way that undermines the
# audit trail this gateway is designed to produce.
#
# ---- Category 4: Security-control bypass ----------------------------------
#
# Branch protection and repo ruleset modifications re-enable force-push
# and remove required reviews -- neutralising the review gates this
# proxy exists to support. Actions secrets/variables and environment
# secrets/variables are indirect code execution: inject material and CI
# runs with it.
#
# ---- Category 5: Account-scope nukes --------------------------------------
#
# `DELETE /user` and user-key deletion can lock the account out entirely.
# ---------------------------------------------------------------------------

# -- Category 1: Persistent access / backdoor installation --

rule "github-deny-create-webhook" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/hooks"
  }
  verdict = "deny"
}

rule "github-deny-create-org-webhook" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/orgs/*/hooks"
  }
  verdict = "deny"
}

rule "github-deny-add-user-ssh-key" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/user/keys"
  }
  verdict = "deny"
}

rule "github-deny-add-deploy-key" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/keys"
  }
  verdict = "deny"
}

rule "github-deny-add-gpg-key" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/user/gpg_keys"
  }
  verdict = "deny"
}

rule "github-deny-add-ssh-signing-key" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/user/ssh_signing_keys"
  }
  verdict = "deny"
}

rule "github-deny-add-collaborator" {
  match {
    host   = "api.github.com"
    method = "PUT"
    path   = "/repos/*/*/collaborators/*"
  }
  verdict = "deny"
}

rule "github-deny-create-org-invitation" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/orgs/*/invitations"
  }
  verdict = "deny"
}

rule "github-deny-set-org-membership" {
  match {
    host   = "api.github.com"
    method = "PUT"
    path   = "/orgs/*/memberships/*"
  }
  verdict = "deny"
}

# -- Category 2: Privacy / visibility changes --

rule "github-deny-edit-repo" {
  match {
    host   = "api.github.com"
    method = "PATCH"
    path   = "/repos/*/*"
  }
  verdict = "deny"
}

rule "github-deny-transfer-repo" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/transfer"
  }
  verdict = "deny"
}

# -- Category 3: Destructive / tracks-covering --

rule "github-deny-delete-repo" {
  match {
    host   = "api.github.com"
    method = "DELETE"
    path   = "/repos/*/*"
  }
  verdict = "deny"
}

rule "github-deny-delete-run" {
  match {
    host   = "api.github.com"
    method = "DELETE"
    path   = "/repos/*/*/actions/runs/*"
  }
  verdict = "deny"
}

rule "github-deny-delete-run-logs" {
  match {
    host   = "api.github.com"
    method = "DELETE"
    path   = "/repos/*/*/actions/runs/*/logs"
  }
  verdict = "deny"
}

rule "github-deny-force-cancel-run" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/actions/runs/*/force-cancel"
  }
  verdict = "deny"
}

# -- Category 4: Security-control bypass --

rule "github-deny-put-branch-protection" {
  match {
    host   = "api.github.com"
    method = "PUT"
    path   = "/repos/*/*/branches/*/protection"
  }
  verdict = "deny"
}

rule "github-deny-delete-branch-protection" {
  match {
    host   = "api.github.com"
    method = "DELETE"
    path   = "/repos/*/*/branches/*/protection"
  }
  verdict = "deny"
}

rule "github-deny-create-repo-ruleset" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/rulesets"
  }
  verdict = "deny"
}

rule "github-deny-update-repo-ruleset" {
  match {
    host   = "api.github.com"
    method = "PUT"
    path   = "/repos/*/*/rulesets/*"
  }
  verdict = "deny"
}

rule "github-deny-put-actions-secret" {
  match {
    host   = "api.github.com"
    method = "PUT"
    path   = "/repos/*/*/actions/secrets/*"
  }
  verdict = "deny"
}

rule "github-deny-delete-actions-secret" {
  match {
    host   = "api.github.com"
    method = "DELETE"
    path   = "/repos/*/*/actions/secrets/*"
  }
  verdict = "deny"
}

rule "github-deny-create-actions-variable" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/actions/variables"
  }
  verdict = "deny"
}

rule "github-deny-update-actions-variable" {
  match {
    host   = "api.github.com"
    method = "PATCH"
    path   = "/repos/*/*/actions/variables/*"
  }
  verdict = "deny"
}

rule "github-deny-put-env-secret" {
  match {
    host   = "api.github.com"
    method = "PUT"
    path   = "/repos/*/*/environments/*/secrets/*"
  }
  verdict = "deny"
}

rule "github-deny-put-env-variable" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/environments/*/variables"
  }
  verdict = "deny"
}

rule "github-deny-update-env-variable" {
  match {
    host   = "api.github.com"
    method = "PATCH"
    path   = "/repos/*/*/environments/*/variables/*"
  }
  verdict = "deny"
}

# -- Category 5: Account-scope nukes --

rule "github-deny-delete-user" {
  match {
    host   = "api.github.com"
    method = "DELETE"
    path   = "/user"
  }
  verdict = "deny"
}

rule "github-deny-delete-user-ssh-key" {
  match {
    host   = "api.github.com"
    method = "DELETE"
    path   = "/user/keys/*"
  }
  verdict = "deny"
}

rule "github-deny-delete-user-signing-key" {
  match {
    host   = "api.github.com"
    method = "DELETE"
    path   = "/user/ssh_signing_keys/*"
  }
  verdict = "deny"
}

rule "github-deny-delete-user-gpg-key" {
  match {
    host   = "api.github.com"
    method = "DELETE"
    path   = "/user/gpg_keys/*"
  }
  verdict = "deny"
}
