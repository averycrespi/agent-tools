package config

import (
	"fmt"
	"strings"
)

// validateConfig is the single entry point for config-level invariants.
// Called from both Load (post-merge) and Save (pre-write) so an invalid
// config is rejected on read AND can never be written from a CLI path.
//
// Add new validations here as separate helpers; keep them named after the
// field they guard so failures point at the offending HCL stanza.
func validateConfig(cfg Config) error {
	if err := validateNoInterceptHosts(cfg.ProxyBehavior.NoInterceptHosts); err != nil {
		return err
	}
	return nil
}

// validateNoInterceptHosts rejects entries that match every (or nearly every)
// host. no_intercept_hosts disables MITM for matching connections — a wildcard
// entry silently disables every rule, every audit row, every injection. Any
// real entry has literal text in it (e.g. "api.example.com", "*.googleapis.com",
// "*.internal"); a pattern composed entirely of "*" and "." characters is
// always either a typo or an intentional global kill-switch, neither of which
// we want to accept.
func validateNoInterceptHosts(patterns []string) error {
	for i, p := range patterns {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			return fmt.Errorf(
				"proxy_behavior.no_intercept_hosts[%d]: pattern is empty",
				i,
			)
		}
		literalCount := 0
		for _, r := range trimmed {
			if r != '*' && r != '.' {
				literalCount++
			}
		}
		if literalCount == 0 {
			return fmt.Errorf(
				"proxy_behavior.no_intercept_hosts[%d]: pattern %q matches every (or nearly every) host; refusing to disable MITM globally. List specific hosts (e.g. \"pinned.example.com\") or label-scoped wildcards (e.g. \"*.internal\")",
				i, p,
			)
		}
	}
	return nil
}
