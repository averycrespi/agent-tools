# Read-only Jira Cloud REST API access using an Atlassian API token.
# v3: https://developer.atlassian.com/cloud/jira/platform/rest/v3/intro/
# v2: https://developer.atlassian.com/cloud/jira/platform/rest/v2/intro/
#
# Allows GETs under /rest/api/3/ and /rest/api/2/ on a single Jira Cloud
# site. v3 is the current recommended version; v2 is still supported and
# used by older integrations. Writes and other paths fall through: the
# sandbox's dummy Authorization header reaches Jira and the request fails
# 401. Drop this file in ~/.config/agent-gateway/rules.d/ and run
# `agent-gateway rules reload`.
#
# The `yoursite` placeholder in `host` below must be edited to match your
# Atlassian site (the subdomain of `.atlassian.net`).
#
# ---- Create an API token ---------------------------------------------------
#
# https://id.atlassian.com/manage-profile/security/api-tokens -> "Create API
# token". Basic auth pairs the token with the Atlassian account email the
# token was created under: `Authorization: Basic base64(email:token)`.
#
# ---- One-time setup on the host --------------------------------------------
#
# The secret value is pre-computed base64(email:token) -- agent-gateway does
# no encoding at injection time. `tr -d '\n'` strips the newline that both
# GNU and BSD `base64` can emit for longer inputs:
#
#   printf '%s' "you@example.com:<api-token>" \
#     | base64 | tr -d '\n' \
#     | agent-gateway secret add jira_basic_auth --host yoursite.atlassian.net
#
# ---- Smoke test inside the sandbox -----------------------------------------
#
#   curl -H "Authorization: Basic dummy" \
#     https://yoursite.atlassian.net/rest/api/3/myself
#
# The dummy Basic value is overwritten at the gateway.
#
# ---- Scope / blast radius --------------------------------------------------
#
# Atlassian API tokens inherit the *full* permissions of the account they
# were issued under -- there is no per-token scoping. To limit what the
# agent can reach, create the token under a dedicated service account
# whose Jira group memberships grant only the project and permission-scheme
# access the agent should have. For true scope-limited auth, use OAuth 2.0
# (3LO) with read scopes like `read:jira-work` and `read:jira-user`; that
# integration pattern uses a different host (api.atlassian.com) and is not
# covered by this example.
#
# ---- Adjacent Atlassian APIs on the same host ------------------------------
#
# The same domain exposes additional APIs that are NOT matched by these rules:
#   /rest/agile/1.0/**   -- Jira Software (boards, sprints, backlog)
#   /rest/servicedeskapi -- Jira Service Management
#   /wiki/rest/**        -- Confluence, if the same Atlassian site has it
# Duplicate a rule and swap the path glob to enable any of these.
# ----------------------------------------------------------------------------

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
