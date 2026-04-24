package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/hostmatch"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/hostnorm"
)

// Upper-bound caps on numeric config fields. These are guardrails against
// self-inflicted footguns (typo'd `99999` instead of `999`, a pasted size
// that decoded differently than intended); they are NOT a security boundary
// against a malicious operator config. An operator who wants more can edit
// the constant and rebuild.
const (
	maxRetentionDays = 3650      // 10 years — well past any realistic audit policy.
	maxBodyBuffer    = 100 << 20 // 100 MiB — above this the request-buffering strategy itself is wrong.
	maxPendingCap    = 10000     // 10k parked approvals — queue memory blows up well before this.
)

// validateConfig is the single entry point for config-level invariants.
// Called from both Load (post-merge) and Save (pre-write) so an invalid
// config is rejected on read AND can never be written from a CLI path.
//
// It mutates cfg in place to store canonical forms (e.g. host-normalized
// no_intercept_hosts entries) and returns any soft-warning messages the
// caller should surface. A non-nil error means the config is unusable.
//
// Add new validations here as separate helpers; keep them named after the
// field they guard so failures point at the offending HCL stanza.
func validateConfig(cfg *Config) ([]string, error) {
	var warnings []string
	w, err := validateNoInterceptHosts(cfg.ProxyBehavior.NoInterceptHosts)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, w...)
	if err := validateListenAddrs(cfg); err != nil {
		return nil, err
	}
	if err := validateBounds(cfg); err != nil {
		return nil, err
	}
	return warnings, nil
}

// validateBounds rejects numeric config fields that exceed their upper cap.
// The caps are deliberately generous — an operator doesn't hit them unless
// they typo'd an extra digit or pasted the wrong size. Errors are aggregated
// via errors.Join so all offending fields are surfaced in a single Load,
// matching the pattern used by validateListenAddrs.
func validateBounds(cfg *Config) error {
	var errs []error
	if cfg.Audit.RetentionDays > maxRetentionDays {
		errs = append(errs, fmt.Errorf(
			"audit.retention_days: %d exceeds cap of %d days",
			cfg.Audit.RetentionDays, maxRetentionDays,
		))
	}
	if cfg.ProxyBehavior.MaxBodyBuffer > maxBodyBuffer {
		errs = append(errs, fmt.Errorf(
			"proxy_behavior.max_body_buffer: %d exceeds cap of %d bytes (100 MiB)",
			cfg.ProxyBehavior.MaxBodyBuffer, maxBodyBuffer,
		))
	}
	if cfg.Approval.MaxPending > maxPendingCap {
		errs = append(errs, fmt.Errorf(
			"approval.max_pending: %d exceeds cap of %d",
			cfg.Approval.MaxPending, maxPendingCap,
		))
	}
	if cfg.Approval.MaxPendingPerAgent < 0 {
		errs = append(errs, fmt.Errorf(
			"approval.max_pending_per_agent: %d is negative",
			cfg.Approval.MaxPendingPerAgent,
		))
	}
	if cfg.Approval.MaxPendingPerAgent > maxPendingCap {
		errs = append(errs, fmt.Errorf(
			"approval.max_pending_per_agent: %d exceeds cap of %d",
			cfg.Approval.MaxPendingPerAgent, maxPendingCap,
		))
	}
	// A per-agent cap above the global cap is nonsensical — the global cap
	// would trip first, making the per-agent cap dead code. Catch the typo
	// here rather than let the operator wonder why it never fires.
	if cfg.Approval.MaxPending > 0 && cfg.Approval.MaxPendingPerAgent > cfg.Approval.MaxPending {
		errs = append(errs, fmt.Errorf(
			"approval.max_pending_per_agent: %d exceeds approval.max_pending: %d",
			cfg.Approval.MaxPendingPerAgent, cfg.Approval.MaxPending,
		))
	}
	return errors.Join(errs...)
}

// validateListenAddrs refuses any listener binding that is not a loopback
// interface. Both the proxy and dashboard carry agent tokens / admin tokens
// over plain HTTP; the loopback-only bind is the load-bearing boundary that
// keeps those tokens off the network. A misconfigured `listen = "0.0.0.0:…"`
// would expose the gateway — and every cached secret it guards — to the LAN.
// Errors from both listeners are aggregated so a single Load surfaces all
// offending stanzas at once instead of forcing a fix-retry-fix cycle.
func validateListenAddrs(cfg *Config) error {
	var errs []error
	if err := ValidateLoopbackAddr(cfg.Proxy.Listen); err != nil {
		errs = append(errs, fmt.Errorf("proxy.listen: %w", err))
	}
	if err := ValidateLoopbackAddr(cfg.Dashboard.Listen); err != nil {
		errs = append(errs, fmt.Errorf("dashboard.listen: %w", err))
	}
	return errors.Join(errs...)
}

// validateNoInterceptHosts rejects entries that match every (or nearly every)
// host. no_intercept_hosts disables MITM for matching connections — a wildcard
// entry silently disables every rule, every audit row, every injection. Any
// real entry has literal text in it (e.g. "api.example.com", "*.googleapis.com",
// "*.internal"); a pattern composed entirely of "*" and "." characters is
// always either a typo or an intentional global kill-switch, neither of which
// we want to accept.
//
// Valid entries are also normalized in place via hostnorm.NormalizeGlob so
// that rule-side and config-side globs use the same canonical form. A
// normalization difference produces a soft warning.
func validateNoInterceptHosts(patterns []string) ([]string, error) {
	var warnings []string
	for i, p := range patterns {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			return nil, fmt.Errorf(
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
			return nil, fmt.Errorf(
				"proxy_behavior.no_intercept_hosts[%d]: pattern %q matches every (or nearly every) host; refusing to disable MITM globally. List specific hosts (e.g. \"pinned.example.com\") or label-scoped wildcards (e.g. \"*.internal\")",
				i, p,
			)
		}
		normalized, err := hostnorm.NormalizeGlob(trimmed)
		if err != nil {
			return nil, fmt.Errorf(
				"proxy_behavior.no_intercept_hosts[%d]: %w",
				i, err,
			)
		}
		if normalized != p {
			warnings = append(warnings, fmt.Sprintf(
				"config: proxy_behavior.no_intercept_hosts[%d] %q normalized to %q",
				i, p, normalized,
			))
			patterns[i] = normalized
		}
		if err := errorIfPublicSuffix(i, patterns[i]); err != nil {
			return nil, err
		}
	}
	return warnings, nil
}

// errorIfPublicSuffix returns an error when a no_intercept_hosts pattern,
// after stripping its leading wildcard labels, reduces to an ICANN-managed
// public suffix. Such an entry would tunnel every host under a registry-
// controlled TLD past MITM — almost always a misconfiguration (e.g. "*.com"
// tunnels the entire internet; "*.co.uk" tunnels every UK commercial domain).
//
// Patterns whose stripped form is not a public suffix (e.g. "*.example.com"),
// or whose suffix is on the private/non-ICANN portion of the PSL (e.g.
// "*.internal", "*.k8s.local"), do not produce an error.
//
// This is a hard error because a public-suffix pattern disables MITM for an
// entire TLD — far too broad to be intentional. Contrast with the normalization
// mismatch path, which is only a soft warning (the pattern still applies,
// just in a slightly different canonical form).
func errorIfPublicSuffix(idx int, pattern string) error {
	ok, suffix := hostmatch.MatchesPublicSuffix(pattern)
	if !ok {
		return nil
	}
	return fmt.Errorf(
		"proxy_behavior.no_intercept_hosts[%d] %q strips to a public suffix %q; list specific domains instead (e.g. \"*.example.com\")",
		idx, pattern, suffix,
	)
}
