# Gated Jira Cloud write access -- every request blocks on dashboard approval.
# https://developer.atlassian.com/cloud/jira/platform/rest/v3/intro/
#
# Each rule matches one operation a coding agent might reasonably need.
# `verdict = "require-approval"` parks the request until a human clicks
# approve/deny in the dashboard (or it times out into a 504). Uses the
# same `jira_basic_auth` secret as 10-jira-readonly.hcl -- keep the
# `host` lines in the two files in sync.
#
# The `*` in `/rest/api/*/...` covers both v2 and v3; Jira exposes the
# same write endpoints under either version. Paths not listed here fall
# through with the sandbox's dummy Authorization and fail 401 at Jira.
#
# ---- Operations included ---------------------------------------------------
#
#   POST   /issue                      create issue
#   PUT    /issue/{key}                edit issue (top-level fields)
#   POST   /issue/{key}/comment        add a comment
#   POST   /issue/{key}/transitions    transition issue state
#   POST   /issueLink                  link two issues
#
# ---- Deliberately NOT included ---------------------------------------------
#
#   DELETE /issue/{key}/comment/{id}   -- deleting comments is rarely what
#                                         an agent should be doing; add the
#                                         rule yourself if you disagree
#   DELETE /issue/{key}                -- destructive; whole-issue removal
#   PUT    /issue/{key}/assignee       -- add your own rule if needed
#   POST   /issue/{key}/attachments    -- multipart upload, different shape
#   /rest/api/*/user, /group, /project/* admin endpoints
#   Workflow / permission-scheme / field-config mutations
# ----------------------------------------------------------------------------

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

