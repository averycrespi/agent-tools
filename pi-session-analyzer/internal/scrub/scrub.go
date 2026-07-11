// Package scrub removes credential values while preserving diagnostic context.
package scrub

import "regexp"

type rule struct {
	name string
	re   *regexp.Regexp
	repl string
}

var rules = []rule{
	{"private_key", regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----.*?-----END (?:[A-Z0-9 ]+ )?PRIVATE KEY-----`), `[REDACTED:private_key]`},
	{"github_token", regexp.MustCompile(`\b(?:gh[opusr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`), `[REDACTED:github_token]`},
	{"aws_access_key", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), `[REDACTED:aws_access_key]`},
	{"slack_token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{20,}\b`), `[REDACTED:slack_token]`},
	{"api_key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`), `[REDACTED:api_key]`},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`), `[REDACTED:jwt]`},
	{"authorization", regexp.MustCompile(`(?i)(["']?\bauthorization\b["']?\s*[:=]\s*["']?(?:bearer|basic)\s+)[A-Za-z0-9._~+/-]+={0,2}`), `${1}[REDACTED:authorization]`},
	{"assignment", regexp.MustCompile(`(?i)(["']?\b(?:api[_-]?key|access[_-]?token|auth[_-]?token|aws[_-]?secret[_-]?access[_-]?key|password|passwd|secret|token)\b["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,"'};]+)`), `${1}[REDACTED:assignment]`},
}

// Scrub replaces supported credential values with stable rule markers.
func Scrub(value string) string {
	for _, r := range rules {
		value = r.re.ReplaceAllString(value, r.repl)
	}
	return value
}
