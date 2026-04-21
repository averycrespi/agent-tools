# Read-only Confluence Cloud REST API access using an Atlassian API token.
# v2: https://developer.atlassian.com/cloud/confluence/rest/v2/intro/
# v1: https://developer.atlassian.com/cloud/confluence/rest/v1/intro/
#
# Allows GETs under /wiki/api/v2/ AND /wiki/rest/api/ on a single
# Atlassian site. v2 is the current recommended API, but its coverage
# is still being filled in -- for example, label management is
# read-only in v2, so label writes need v1. Keeping v1 readable here
# lets an agent mix-and-match as needed. Writes and other paths fall
# through with the sandbox's dummy Authorization and fail 401.
#
# Drop this file in ~/.config/agent-gateway/rules.d/ and run
# `agent-gateway rules reload`. Edit `yoursite` on the `host` lines
# below to match your Atlassian site.
#
# ---- Create an API token ---------------------------------------------------
#
# https://id.atlassian.com/manage-profile/security/api-tokens -> "Create API
# token". Atlassian API tokens work across Jira, Confluence, and other
# products on the same site, so if you already have `jira_basic_auth`
# set up from 10-jira-readonly.hcl for this site, you can either:
#   - create a separate `confluence_basic_auth` secret with the same value
#     (recommended -- keeps each example file self-contained), or
#   - swap `${secrets.confluence_basic_auth}` below for
#     `${secrets.jira_basic_auth}`.
#
# ---- One-time setup on the host --------------------------------------------
#
#   printf '%s' "you@example.com:<api-token>" \
#     | base64 | tr -d '\n' \
#     | agent-gateway secret add confluence_basic_auth --host yoursite.atlassian.net
#
# ---- Smoke test inside the sandbox -----------------------------------------
#
#   curl -H "Authorization: Basic dummy" \
#     "https://yoursite.atlassian.net/wiki/api/v2/spaces?limit=1"
#
# ---- Scope / blast radius --------------------------------------------------
#
# Atlassian API tokens inherit the *full* permissions of the account
# they were issued under. To limit what the agent can read, create the
# token under a dedicated service account whose Confluence space
# permissions grant only the content the agent should see. For
# scope-limited auth, use OAuth 2.0 (3LO) with read scopes like
# `read:confluence-content.all`, `read:confluence-space.summary`; that
# integration pattern uses a different host (api.atlassian.com) and is
# not covered by this example.
# ----------------------------------------------------------------------------

rule "confluence-readonly-v2" {
  match {
    host   = "yoursite.atlassian.net"
    method = "GET"
    path   = "/wiki/api/v2/**"
  }

  verdict = "allow"

  inject {
    replace_header = {
      "Authorization" = "Basic ${secrets.confluence_basic_auth}"
    }
  }
}

rule "confluence-readonly-v1" {
  match {
    host   = "yoursite.atlassian.net"
    method = "GET"
    path   = "/wiki/rest/api/**"
  }

  verdict = "allow"

  inject {
    replace_header = {
      "Authorization" = "Basic ${secrets.confluence_basic_auth}"
    }
  }
}
