rule "github-issue-create" {
  agents = ["claude-review"]

  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/issues"

    headers = {
      "X-GitHub-Api-Version" = "^2022-"
    }
  }

  verdict = "allow"

  inject {
    set_header = {
      "Authorization" = "Bearer ${secrets.gh_bot}"
    }
  }
}
