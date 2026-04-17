rule "github-issue-label-check" {
  agents = ["claude-review"]

  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/issues"

    json_body {
      jsonpath "$.title" {
        matches = "^\\[bot\\]"
      }
      jsonpath "$.labels[*]" {
        matches = "^automation$"
      }
    }
  }

  verdict = "allow"
}
