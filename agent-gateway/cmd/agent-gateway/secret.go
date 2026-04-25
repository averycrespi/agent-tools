package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/agents"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/daemon"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/secrets"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/store"
)

// stdinIsTTY is a package-level variable for testing stdin TTY detection.
// Tests may override this to avoid needing a real TTY.
var stdinIsTTY = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// openSecretStore opens a short-lived secrets store using the state DB.
// Callers must close the returned *sql.DB when done.
func openSecretStore() (secrets.Store, func(), error) {
	db, err := store.Open(paths.StateDB())
	if err != nil {
		return nil, nil, fmt.Errorf("open state db: %w", err)
	}
	s, err := secrets.NewStore(db, slog.Default())
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("create secrets store: %w", err)
	}
	return s, func() { _ = db.Close() }, nil
}

// sendHUP sends SIGHUP to the daemon (if running) and tolerates all errors.
func sendHUP(pidPath string) error {
	err := daemon.SignalDaemon(pidPath)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	// Stale PID, wrong comm, etc. — all tolerated: the CLI write already
	// succeeded, and the daemon picks up the new state on next start.
	return nil
}

// readStdinValue reads a value from stdin, trimming the trailing newline.
// Returns an error when isTTY is true.
func readStdinValue(in io.Reader, isTTY bool) (string, error) {
	if isTTY {
		return "", fmt.Errorf("must pipe value in (stdin is a TTY)")
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return strings.TrimRight(string(data), "\n"), nil
}

// printCoverageAfterMutation loads the rules engine from rulesDir and prints
// any coverage warnings to out. Non-fatal — failures to build the engine are
// silently ignored so the mutation command always succeeds.
func printCoverageAfterMutation(ctx context.Context, out io.Writer, store secrets.Store, rulesDir string) {
	engine, err := rules.NewEngine(rulesDir)
	if err != nil {
		return
	}
	for _, w := range warnSecretCoverage(ctx, engine, store) {
		_, _ = fmt.Fprintln(out, "warning:", w)
	}
}

// newSecretCmd builds the "secret" command tree.
func newSecretCmd() *cobra.Command {
	secretCmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage encrypted secrets",
	}

	secretCmd.AddCommand(newSecretAddCmd())
	secretCmd.AddCommand(newSecretListCmd())
	secretCmd.AddCommand(newSecretUpdateCmd())
	secretCmd.AddCommand(newSecretRMCmd())
	secretCmd.AddCommand(newSecretBindCmd())
	secretCmd.AddCommand(newSecretUnbindCmd())

	return secretCmd
}

// newSecretAddCmd returns the "secret add <name>" command.
func newSecretAddCmd() *cobra.Command {
	var (
		agent string
		desc  string
		hosts []string
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Store a secret value (reads from stdin)",
		Long: "Store a secret value read from stdin. Every secret must be bound\n" +
			"to at least one host glob via --host (repeatable). Use --host \"**\"\n" +
			"for an explicit all-hosts binding.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(hosts) == 0 {
				return fmt.Errorf("at least one --host is required (use --host \"**\" to allow every host)")
			}
			s, cleanup, err := openSecretStore()
			if err != nil {
				return err
			}
			defer cleanup()

			// Agent registry is only consulted when --agent is set; open it
			// lazily so callers that don't scope to an agent don't pay the
			// argon2-table load.
			var r agents.Registry
			if agent != "" {
				reg, regCleanup, err := openAgentRegistry()
				if err != nil {
					return err
				}
				defer regCleanup()
				r = reg
			}

			return execSecretAdd(
				cmd.Context(),
				s,
				r,
				args[0],
				agent,
				desc,
				hosts,
				cmd.InOrStdin(),
				cmd.OutOrStdout(),
				stdinIsTTY(),
				func(pidPath string) error { return sendHUP(pidPath) },
				paths.RulesDir(),
			)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent name (omit for global scope)")
	cmd.Flags().StringVar(&desc, "desc", "", "human-readable description")
	cmd.Flags().StringSliceVar(&hosts, "host", nil, "host glob the secret may be injected into (repeatable, required)")
	return cmd
}

// duplicateSecretError formats the user-facing error returned when
// "secret add" is invoked for an (name, scope) pair that already exists.
// The message names the existing secret and points at "secret update" as
// the right way to change its value.
func duplicateSecretError(name, agent string) error {
	if agent == "" {
		return fmt.Errorf("secret %q already exists. To change its value, use: agent-gateway secret update %s", name, name)
	}
	return fmt.Errorf("secret %q already exists for agent %q. To change its value, use: agent-gateway secret update %s --agent %s", name, agent, name, agent)
}

// execSecretAdd implements "secret add". Separated for testability.
// signalFn receives the PID file path and is responsible for sending SIGHUP.
// r may be nil when agent is empty (global scope).
func execSecretAdd(
	ctx context.Context,
	s secrets.Store,
	r agents.Registry,
	name, agent, desc string,
	hosts []string,
	in io.Reader,
	out io.Writer,
	isTTY bool,
	signalFn func(string) error,
	rulesDir string,
) error {
	// Fail early if the target agent doesn't exist — a dangling agent-scoped
	// secret can never resolve at runtime, so it's always a typo or a missed
	// "agent add" step.
	if agent != "" {
		metas, err := r.List(ctx)
		if err != nil {
			return fmt.Errorf("secret add: list agents: %w", err)
		}
		found := false
		for _, m := range metas {
			if m.Name == agent {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("agent %q does not exist. To register it, use: agent-gateway agent add %s", agent, agent)
		}
	}

	value, err := readStdinValue(in, isTTY)
	if err != nil {
		return err
	}

	if err := s.Set(ctx, name, agent, value, desc, hosts); err != nil {
		if errors.Is(err, secrets.ErrDuplicate) {
			return duplicateSecretError(name, agent)
		}
		return fmt.Errorf("secret add: %w", err)
	}

	_, _ = fmt.Fprintf(out, "added: %s\n", name)

	// Shadow warning: if agent-scoped, check whether a global row also exists.
	if agent != "" {
		_, _, _, globalErr := s.Get(ctx, name, "")
		if globalErr == nil {
			// A global row exists for this name — print shadow warning.
			_, _ = fmt.Fprintf(out,
				"warning: secret %q is also set globally — the agent-scoped value will shadow it for agent %q\n",
				name, agent,
			)
		}
	}

	printCoverageAfterMutation(ctx, out, s, rulesDir)
	_ = signalFn(paths.PIDFile())
	return nil
}

// newSecretBindCmd returns the "secret bind <name>" command.
func newSecretBindCmd() *cobra.Command {
	var (
		agent string
		hosts []string
	)
	cmd := &cobra.Command{
		Use:   "bind <name>",
		Short: "Add host globs to a secret's allowed_hosts list",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(hosts) == 0 {
				return fmt.Errorf("at least one --host is required")
			}
			s, cleanup, err := openSecretStore()
			if err != nil {
				return err
			}
			defer cleanup()
			return execSecretBind(cmd.Context(), s, args[0], agent, hosts, cmd.OutOrStdout(), sendHUP, paths.RulesDir())
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent name (omit for global scope)")
	cmd.Flags().StringSliceVar(&hosts, "host", nil, "host glob to add (repeatable)")
	return cmd
}

// execSecretBind implements "secret bind". Separated for testability.
func execSecretBind(
	ctx context.Context,
	s secrets.Store,
	name, agent string,
	hosts []string,
	out io.Writer,
	signalFn func(string) error,
	rulesDir string,
) error {
	if err := s.Bind(ctx, name, agent, hosts); err != nil {
		return fmt.Errorf("secret bind: %w", err)
	}
	_, _ = fmt.Fprintf(out, "bound: %s\n", name)
	printCoverageAfterMutation(ctx, out, s, rulesDir)
	_ = signalFn(paths.PIDFile())
	return nil
}

// newSecretUnbindCmd returns the "secret unbind <name>" command.
func newSecretUnbindCmd() *cobra.Command {
	var (
		agent string
		hosts []string
	)
	cmd := &cobra.Command{
		Use:   "unbind <name>",
		Short: "Remove host globs from a secret's allowed_hosts list",
		Long: "Remove host globs from a secret's allowed_hosts list. Fails if\n" +
			"the removal would leave the list empty — rebind first, or use\n" +
			"`secret rm` to delete the secret entirely.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(hosts) == 0 {
				return fmt.Errorf("at least one --host is required")
			}
			s, cleanup, err := openSecretStore()
			if err != nil {
				return err
			}
			defer cleanup()
			return execSecretUnbind(cmd.Context(), s, args[0], agent, hosts, cmd.OutOrStdout(), sendHUP, paths.RulesDir())
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent name (omit for global scope)")
	cmd.Flags().StringSliceVar(&hosts, "host", nil, "host glob to remove (repeatable)")
	return cmd
}

// execSecretUnbind implements "secret unbind". Separated for testability.
func execSecretUnbind(
	ctx context.Context,
	s secrets.Store,
	name, agent string,
	hosts []string,
	out io.Writer,
	signalFn func(string) error,
	rulesDir string,
) error {
	if err := s.Unbind(ctx, name, agent, hosts); err != nil {
		return fmt.Errorf("secret unbind: %w", err)
	}
	_, _ = fmt.Fprintf(out, "unbound: %s\n", name)
	printCoverageAfterMutation(ctx, out, s, rulesDir)
	_ = signalFn(paths.PIDFile())
	return nil
}

// newSecretListCmd returns the "secret list" command.
func newSecretListCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List secrets (metadata only, no values)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cleanup, err := openSecretStore()
			if err != nil {
				return err
			}
			defer cleanup()
			return execSecretList(cmd.Context(), s, output, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format: text or json")
	return cmd
}

// execSecretList implements "secret list". Separated for testability.
func execSecretList(ctx context.Context, s secrets.Store, output string, out io.Writer) error {
	metas, err := s.List(ctx)
	if err != nil {
		return fmt.Errorf("secret list: %w", err)
	}
	switch output {
	case "", "text":
		return writeSecretListText(out, metas)
	case "json":
		return writeSecretListJSON(out, metas)
	default:
		return fmt.Errorf("secret list: --output must be 'text' or 'json', got %q", output)
	}
}

// writeSecretListText writes the tab-separated text table of secrets.
func writeSecretListText(out io.Writer, metas []secrets.Metadata) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tSCOPE\tHOSTS\tCREATED\tROTATED\tLAST-USED\tDESCRIPTION")
	for _, m := range metas {
		lastUsed := "-"
		if m.LastUsedAt != nil {
			lastUsed = m.LastUsedAt.UTC().Format(time.RFC3339)
		}
		hosts := strings.Join(m.AllowedHosts, ",")
		if hosts == "" {
			hosts = "-"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			m.Name,
			m.Scope,
			hosts,
			m.CreatedAt.UTC().Format(time.RFC3339),
			m.RotatedAt.UTC().Format(time.RFC3339),
			lastUsed,
			m.Description,
		)
	}
	return w.Flush()
}

// secretJSON is the JSON representation of a secret for "secret list --output json".
// Only the 6 public metadata fields are included; sensitive fields (ciphertext,
// nonce, description) and internal fields (id) are intentionally omitted.
// bound_rules is also omitted — it is not in Metadata and computing it would
// be scope creep.
type secretJSON struct {
	Name         string   `json:"name"`
	Scope        string   `json:"scope"`
	AllowedHosts []string `json:"allowed_hosts"`
	CreatedAt    string   `json:"created_at"`
	RotatedAt    string   `json:"rotated_at"`
	LastUsedAt   *string  `json:"last_used_at"`
}

// writeSecretListJSON encodes the secret list as {"secrets": [...]}.
func writeSecretListJSON(out io.Writer, metas []secrets.Metadata) error {
	items := make([]secretJSON, 0, len(metas))
	for _, m := range metas {
		item := secretJSON{
			Name:         m.Name,
			Scope:        m.Scope,
			AllowedHosts: m.AllowedHosts,
			CreatedAt:    m.CreatedAt.UTC().Format(time.RFC3339),
			RotatedAt:    m.RotatedAt.UTC().Format(time.RFC3339),
		}
		if m.LastUsedAt != nil {
			s := m.LastUsedAt.UTC().Format(time.RFC3339)
			item.LastUsedAt = &s
		}
		items = append(items, item)
	}
	payload := struct {
		Secrets []secretJSON `json:"secrets"`
	}{Secrets: items}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// newSecretUpdateCmd returns the "secret update <name>" command.
func newSecretUpdateCmd() *cobra.Command {
	var (
		agent string
		force bool
	)
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update the value of an existing secret (reads new value from stdin)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, cleanup, err := openSecretStore()
			if err != nil {
				return err
			}
			defer cleanup()
			confirmFn := func() (bool, error) {
				return confirmViaTTY(cmd.OutOrStdout(), force,
					fmt.Sprintf("Update secret %q? The previous value will be overwritten.", args[0]))
			}
			return execSecretUpdate(
				cmd.Context(),
				s,
				args[0],
				agent,
				cmd.InOrStdin(),
				cmd.OutOrStdout(),
				stdinIsTTY(),
				confirmFn,
				func(pidPath string) error { return sendHUP(pidPath) },
				paths.RulesDir(),
			)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent name (omit for global scope)")
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompt")
	return cmd
}

// execSecretUpdate implements "secret update". Separated for testability.
func execSecretUpdate(
	ctx context.Context,
	s secrets.Store,
	name, agent string,
	in io.Reader,
	out io.Writer,
	isTTY bool,
	confirmFn func() (bool, error),
	signalFn func(string) error,
	rulesDir string,
) error {
	newValue, err := readStdinValue(in, isTTY)
	if err != nil {
		return err
	}

	ok, err := confirmFn()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	if err := s.Rotate(ctx, name, agent, newValue); err != nil {
		return fmt.Errorf("secret update: %w", err)
	}

	_, _ = fmt.Fprintf(out, "updated: %s\n", name)
	printCoverageAfterMutation(ctx, out, s, rulesDir)
	_ = signalFn(paths.PIDFile())
	return nil
}

// newSecretRMCmd returns the "secret rm <name>" command.
func newSecretRMCmd() *cobra.Command {
	var (
		agent string
		force bool
	)
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Delete a secret",
		Long: `Removes the secret from the encrypted store.

Immediate consequences:
  - Rules referencing ${secrets.<name>} will fail with 403 Forbidden and
    X-Agent-Gateway-Reason: secret-unresolved on the next request that matches
    them.

Recovery:
  The encrypted value is deleted; if you have not saved the plaintext
  elsewhere, recovery is not possible — re-add the secret with
  'agent-gateway secret add <name>' using a fresh credential value.`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, cleanup, err := openSecretStore()
			if err != nil {
				return err
			}
			defer cleanup()
			confirmFn := func() (bool, error) {
				return confirm(cmd.InOrStdin(), cmd.OutOrStdout(), stdinIsTTY(), force,
					fmt.Sprintf("Delete secret %q?", args[0]))
			}
			return execSecretRM(
				cmd.Context(),
				s,
				args[0],
				agent,
				cmd.OutOrStdout(),
				confirmFn,
				func(pidPath string) error { return sendHUP(pidPath) },
				paths.RulesDir(),
			)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent name (omit for global scope)")
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompt")
	return cmd
}

// execSecretRM implements "secret rm". Separated for testability.
func execSecretRM(
	ctx context.Context,
	s secrets.Store,
	name, agent string,
	out io.Writer,
	confirmFn func() (bool, error),
	signalFn func(string) error,
	rulesDir string,
) error {
	// Fail early if the (name, scope) pair doesn't exist — confirming the
	// removal of something that isn't there would just be a confusing ritual.
	metas, err := s.List(ctx)
	if err != nil {
		return fmt.Errorf("secret rm: %w", err)
	}
	wantScope := "global"
	if agent != "" {
		wantScope = "agent:" + agent
	}
	found := false
	for _, m := range metas {
		if m.Name == name && m.Scope == wantScope {
			found = true
			break
		}
	}
	if !found {
		return secrets.ErrNotFound
	}

	ok, err := confirmFn()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if err := s.Delete(ctx, name, agent); err != nil {
		return fmt.Errorf("secret rm: %w", err)
	}
	_, _ = fmt.Fprintf(out, "deleted: %s\n", name)
	printCoverageAfterMutation(ctx, out, s, rulesDir)
	_ = signalFn(paths.PIDFile())
	return nil
}
