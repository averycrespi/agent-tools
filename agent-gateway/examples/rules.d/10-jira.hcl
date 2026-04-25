# Jira Cloud REST API -- reads allowed, writes gated on dashboard approval.
# v3: https://developer.atlassian.com/cloud/jira/platform/rest/v3/intro/
# v2: https://developer.atlassian.com/cloud/jira/platform/rest/v2/intro/
#
# Covers GETs under /rest/api/{2,3}/ (allow) plus a curated set of write
# operations (require-approval) on a single Jira Cloud site. v3 is the
# current recommended version; v2 is still supported and used by older
# integrations. Writes listed below hit /rest/api/*/... so either version
# works. Paths not listed here fall through with the sandbox's dummy
# Authorization and fail 401 at Jira.
#
# Drop this file in ~/.config/agent-gateway/rules.d/ and run
# `agent-gateway reload`. Edit `yoursite` on every `host` line
# below to match your Atlassian site (the subdomain of `.atlassian.net`).
#
# ---- Create an API token --------------------------------------------------
#
# https://id.atlassian.com/manage-profile/security/api-tokens -> "Create
# API token". Basic auth pairs the token with the Atlassian account
# email the token was created under:
# `Authorization: Basic base64(email:token)`.
#
# ---- One-time setup on the host -------------------------------------------
#
# The secret value is pre-computed base64(email:token) -- agent-gateway
# does no encoding at injection time. `tr -d '\n'` strips the newline
# that both GNU and BSD `base64` can emit for longer inputs:
#
#   printf '%s' "you@example.com:<api-token>" \
#     | base64 | tr -d '\n' \
#     | agent-gateway secret add jira_basic_auth --host yoursite.atlassian.net
#
# Without this, requests matched by these rules will fail with HTTP 403 and
# header `X-Agent-Gateway-Reason: secret-unresolved` — the rule matched but
# the gateway had no `${secrets.jira_basic_auth}` to inject.
#
# ---- Smoke test inside the sandbox ----------------------------------------
#
#   curl -H "Authorization: Basic dummy" \
#     https://yoursite.atlassian.net/rest/api/3/myself
#
# The dummy Basic value is overwritten at the gateway.
#
# ---- Scope / blast radius -------------------------------------------------
#
# Atlassian API tokens inherit the *full* permissions of the account
# they were issued under -- there is no per-token scoping. To limit what
# the agent can reach, create the token under a dedicated service
# account whose Jira group memberships grant only the project and
# permission-scheme access the agent should have. For true scope-limited
# auth, use OAuth 2.0 (3LO) with read scopes like `read:jira-work` and
# `read:jira-user`; that integration pattern uses a different host
# (api.atlassian.com) and is not covered by this example.
#
# ---- Adjacent Atlassian APIs on the same host ----------------------------
#
# The same domain exposes additional APIs that are NOT matched by these
# rules:
#
#   /rest/agile/1.0/**   -- Jira Software (boards, sprints, backlog)
#   /rest/servicedeskapi -- Jira Service Management
#   /wiki/rest/**        -- Confluence, if the same Atlassian site has it
#                           (see 10-confluence.hcl)
#
# Duplicate a rule and swap the path glob to enable any of these.
#
# ---- Write operations included --------------------------------------------
#
#   POST   /issue                      create issue
#   PUT    /issue/{key}                edit issue (top-level fields)
#   POST   /issue/{key}/comment        add a comment
#   POST   /issue/{key}/transitions    transition issue state
#   POST   /issueLink                  link two issues
#
# ---- Deliberately NOT included --------------------------------------------
#
#   DELETE /issue/{key}/comment/{id}   -- deleting comments is rarely
#                                         what an agent should be doing;
#                                         add the rule yourself if you
#                                         disagree.
#   DELETE /issue/{key}                -- destructive; whole-issue
#                                         removal.
#   PUT    /issue/{key}/assignee       -- add your own rule if needed.
#   POST   /issue/{key}/attachments    -- multipart upload, different
#                                         shape.
#   /rest/api/*/user, /group, /project/* admin endpoints.
#   Workflow / permission-scheme / field-config mutations.
# ---------------------------------------------------------------------------

# -- Reads --

rule "jira-readonly-v3" {
  match {
    host   = "yoursite.atlassian.net"
    method = "GET"
    path   = "/rest/api/3/**"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Basic ${secrets.jira_basic_auth}"
    }
  }
}

rule "jira-readonly-v2" {
  match {
    host   = "yoursite.atlassian.net"
    method = "GET"
    path   = "/rest/api/2/**"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Basic ${secrets.jira_basic_auth}"
    }
  }
}

# -- Writes (require dashboard approval) --

rule "jira-create-issue" {
  match {
    host   = "yoursite.atlassian.net"
    method = "POST"
    path   = "/rest/api/*/issue"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Basic ${secrets.jira_basic_auth}"
    }
  }
}

rule "jira-edit-issue" {
  match {
    host   = "yoursite.atlassian.net"
    method = "PUT"
    path   = "/rest/api/*/issue/*"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Basic ${secrets.jira_basic_auth}"
    }
  }
}

rule "jira-add-comment" {
  match {
    host   = "yoursite.atlassian.net"
    method = "POST"
    path   = "/rest/api/*/issue/*/comment"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Basic ${secrets.jira_basic_auth}"
    }
  }
}

rule "jira-transition-issue" {
  match {
    host   = "yoursite.atlassian.net"
    method = "POST"
    path   = "/rest/api/*/issue/*/transitions"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Basic ${secrets.jira_basic_auth}"
    }
  }
}

rule "jira-link-issues" {
  match {
    host   = "yoursite.atlassian.net"
    method = "POST"
    path   = "/rest/api/*/issueLink"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Basic ${secrets.jira_basic_auth}"
    }
  }
}
