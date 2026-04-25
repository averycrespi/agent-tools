package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/config"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

func newConfigCmd(configPath func() string) *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Manage agent-gateway configuration",
	}

	configCmd.AddCommand(newConfigPathCmd(configPath))
	configCmd.AddCommand(newConfigRefreshCmd(configPath))
	configCmd.AddCommand(newConfigEditCmd(configPath))

	return configCmd
}

func newConfigPathCmd(configPath func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), configPath())
		},
	}
}

func newConfigRefreshCmd(configPath func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Refresh config file with current defaults",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return config.Refresh(configPath())
		},
	}
}

func newConfigEditCmd(configPath func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the config file in your editor",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return execConfigEdit(configPath(), cmd.OutOrStdout())
		},
	}
}

// execConfigEdit is the testable core of "config edit". It refreshes the
// config file, opens it in $EDITOR (falling back to vi), and then warns the
// operator about any restart-required fields that changed. Output is written
// to out.
func execConfigEdit(cfgPath string, out io.Writer) error {
	if err := config.Refresh(cfgPath); err != nil {
		return err
	}

	// Pre-load: capture state before editor opens. If parsing fails we still
	// let the editor open (the user may be fixing a broken file), but we skip
	// the diff.
	pre, _, preErr := config.Load(cfgPath)
	if preErr != nil {
		_, _ = fmt.Fprintf(out, "note: could not parse config before editing: %v\n", preErr)
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, cfgPath) //nolint:gosec // editor is user-controlled via $EDITOR
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("config edit: editor: %w", err)
	}

	// Skip diff if pre-parse failed — we have nothing to compare against.
	if preErr != nil {
		return nil
	}

	// Post-load: parse the (possibly modified) file.
	post, _, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("config edit: %w", err)
	}

	diffs := diffConfig(&pre, &post)
	if len(diffs) == 0 {
		return nil
	}

	_, _ = fmt.Fprintln(out, "warning: config.hcl has changed. These edits require a daemon restart:")
	for _, d := range diffs {
		_, _ = fmt.Fprintf(out, "  - %s\n", d)
	}
	_, _ = fmt.Fprintf(out,
		"  Apply with: kill $(cat %s) and re-run 'agent-gateway serve'.\n",
		paths.PIDFile(),
	)
	return nil
}

// diffConfig returns a slice of "<field>: <old> -> <new>" strings for every
// restart-required field that differs between pre and post. It does not use
// reflection — fields are enumerated explicitly so only known
// restart-required fields are flagged.
func diffConfig(pre, post *config.Config) []string {
	var diffs []string

	add := func(name, oldVal, newVal string) {
		if oldVal != newVal {
			diffs = append(diffs, fmt.Sprintf("%s: %s -> %s", name, oldVal, newVal))
		}
	}
	addDur := func(name string, oldVal, newVal interface{ String() string }) {
		add(name, oldVal.String(), newVal.String())
	}
	addInt := func(name string, oldVal, newVal int) {
		if oldVal != newVal {
			diffs = append(diffs, fmt.Sprintf("%s: %d -> %d", name, oldVal, newVal))
		}
	}
	addInt64 := func(name string, oldVal, newVal int64) {
		if oldVal != newVal {
			diffs = append(diffs, fmt.Sprintf("%s: %d -> %d", name, oldVal, newVal))
		}
	}
	addBool := func(name string, oldVal, newVal bool) {
		if oldVal != newVal {
			diffs = append(diffs, fmt.Sprintf("%s: %v -> %v", name, oldVal, newVal))
		}
	}
	addHosts := func(name string, oldVal, newVal []string) {
		o := "[" + strings.Join(oldVal, ", ") + "]"
		n := "[" + strings.Join(newVal, ", ") + "]"
		add(name, o, n)
	}

	// Listener addresses — restart required to rebind sockets.
	add("proxy.listen", pre.Proxy.Listen, post.Proxy.Listen)
	add("dashboard.listen", pre.Dashboard.Listen, post.Dashboard.Listen)
	addBool("dashboard.open_browser", pre.Dashboard.OpenBrowser, post.Dashboard.OpenBrowser)

	// Timeouts — applied at serve startup, not reloadable.
	addDur("timeouts.connect_read_header", pre.Timeouts.ConnectReadHeader, post.Timeouts.ConnectReadHeader)
	addDur("timeouts.mitm_handshake", pre.Timeouts.MITMHandshake, post.Timeouts.MITMHandshake)
	addDur("timeouts.idle_keepalive", pre.Timeouts.IdleKeepalive, post.Timeouts.IdleKeepalive)
	addDur("timeouts.upstream_dial", pre.Timeouts.UpstreamDial, post.Timeouts.UpstreamDial)
	addDur("timeouts.upstream_tls", pre.Timeouts.UpstreamTLS, post.Timeouts.UpstreamTLS)
	addDur("timeouts.upstream_response_header", pre.Timeouts.UpstreamResponseHeader, post.Timeouts.UpstreamResponseHeader)
	addDur("timeouts.upstream_idle_keepalive", pre.Timeouts.UpstreamIdleKeepalive, post.Timeouts.UpstreamIdleKeepalive)
	addDur("timeouts.body_buffer_read", pre.Timeouts.BodyBufferRead, post.Timeouts.BodyBufferRead)

	// Proxy behaviour — applied at serve startup.
	addHosts("proxy_behavior.no_intercept_hosts", pre.ProxyBehavior.NoInterceptHosts, post.ProxyBehavior.NoInterceptHosts)
	addInt64("proxy_behavior.max_body_buffer", pre.ProxyBehavior.MaxBodyBuffer, post.ProxyBehavior.MaxBodyBuffer)
	addBool("proxy_behavior.allow_private_upstream", pre.ProxyBehavior.AllowPrivateUpstream, post.ProxyBehavior.AllowPrivateUpstream)

	// Approval — broker is constructed at startup with these values.
	addDur("approval.timeout", pre.Approval.Timeout, post.Approval.Timeout)
	addInt("approval.max_pending", pre.Approval.MaxPending, post.Approval.MaxPending)
	addInt("approval.max_pending_per_agent", pre.Approval.MaxPendingPerAgent, post.Approval.MaxPendingPerAgent)

	// Secrets cache TTL — applied at startup.
	addDur("secrets.cache_ttl", pre.Secrets.CacheTTL, post.Secrets.CacheTTL)

	// Audit retention — prune job is scheduled at startup.
	addInt("audit.retention_days", pre.Audit.RetentionDays, post.Audit.RetentionDays)
	add("audit.prune_at", pre.Audit.PruneAt, post.Audit.PruneAt)

	// Log level/format — slog handler is configured at startup.
	add("log.level", pre.Log.Level, post.Log.Level)
	add("log.format", pre.Log.Format, post.Log.Format)

	return diffs
}
