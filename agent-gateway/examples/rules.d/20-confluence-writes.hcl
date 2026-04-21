# Gated Confluence Cloud write access -- every request blocks on dashboard
# approval. https://developer.atlassian.com/cloud/confluence/rest/v2/intro/
#
# Each rule matches one v2 operation a coding agent might reasonably
# need. `verdict = "require-approval"` parks the request until a human
# clicks approve/deny in the dashboard (or it times out into a 504).
# Uses the same `confluence_basic_auth` secret as 10-confluence-readonly.hcl
# -- keep the `host` lines in the two files in sync.
#
# Scope is v2 only. If the agent needs a write that v2 doesn't cover yet
# (notably adding labels, which is v1-only -- POST /wiki/rest/api/content/
# {id}/label), add a rule for the v1 endpoint separately.
#
# ---- Operations included ---------------------------------------------------
#
#   POST /wiki/api/v2/pages                  create page
#   PUT  /wiki/api/v2/pages/{id}             replace page content
#   PUT  /wiki/api/v2/pages/{id}/title       rename page (title-only)
#   POST /wiki/api/v2/footer-comments        add a page / blogpost comment
#   POST /wiki/api/v2/inline-comments        add a text-anchored comment
#
# ---- Deliberately NOT included ---------------------------------------------
#
#   DELETE /wiki/api/v2/pages/{id}           -- destructive; whole-page delete
#   PUT    /wiki/api/v2/footer-comments/{id} -- editing comments after post
#   PUT    /wiki/api/v2/inline-comments/{id} -- editing comments after post
#   DELETE .../footer-comments, .../inline-comments -- comment removal
#   /wiki/api/v2/blogposts/**                -- blogpost writes; uncommon
#   /wiki/api/v2/attachments/**              -- multipart uploads
#   /wiki/api/v2/spaces/**                   -- space admin
# ----------------------------------------------------------------------------

rule "confluence-create-page" {
  match {
    host   = "yoursite.atlassian.net"
    method = "POST"
    path   = "/wiki/api/v2/pages"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Basic ${secrets.confluence_basic_auth}"
    }
  }
}

rule "confluence-update-page" {
  match {
    host   = "yoursite.atlassian.net"
    method = "PUT"
    path   = "/wiki/api/v2/pages/*"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Basic ${secrets.confluence_basic_auth}"
    }
  }
}

rule "confluence-rename-page" {
  match {
    host   = "yoursite.atlassian.net"
    method = "PUT"
    path   = "/wiki/api/v2/pages/*/title"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Basic ${secrets.confluence_basic_auth}"
    }
  }
}

rule "confluence-add-footer-comment" {
  match {
    host   = "yoursite.atlassian.net"
    method = "POST"
    path   = "/wiki/api/v2/footer-comments"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Basic ${secrets.confluence_basic_auth}"
    }
  }
}

rule "confluence-add-inline-comment" {
  match {
    host   = "yoursite.atlassian.net"
    method = "POST"
    path   = "/wiki/api/v2/inline-comments"
  }
  verdict = "require-approval"
  inject {
    replace_header = {
      "Authorization" = "Basic ${secrets.confluence_basic_auth}"
    }
  }
}
