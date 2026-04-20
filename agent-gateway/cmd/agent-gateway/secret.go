package main

import (
	"context"
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

	"github.com/averycrespi/agent-tools/agent-gateway/internal/daemon"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
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

			return execSecretAdd(
				cmd.Context(),
				s,
				args[0],
				agent,
				desc,
				hosts,
				cmd.InOrStdin(),
				cmd.OutOrStdout(),
				stdinIsTTY(),
				func(pidPath string) error { return sendHUP(pidPath) },
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
func execSecretAdd(
	ctx context.Context,
	s secrets.Store,
	name, agent, desc string,
	hosts []string,
	in io.Reader,
	out io.Writer,
	isTTY bool,
	signalFn func(string) error,
) error {
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
			if err := s.Bind(cmd.Context(), args[0], agent, hosts); err != nil {
				return fmt.Errorf("secret bind: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "bound: %s\n", args[0])
			_ = sendHUP(paths.PIDFile())
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent name (omit for global scope)")
	cmd.Flags().StringSliceVar(&hosts, "host", nil, "host glob to add (repeatable)")
	return cmd
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
			if err := s.Unbind(cmd.Context(), args[0], agent, hosts); err != nil {
				return fmt.Errorf("secret unbind: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "unbound: %s\n", args[0])
			_ = sendHUP(paths.PIDFile())
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent name (omit for global scope)")
	cmd.Flags().StringSliceVar(&hosts, "host", nil, "host glob to remove (repeatable)")
	return cmd
}

// newSecretListCmd returns the "secret list" command.
func newSecretListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List secrets (metadata only, no values)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cleanup, err := openSecretStore()
			if err != nil {
				return err
			}
			defer cleanup()
			return execSecretList(cmd.Context(), s, cmd.OutOrStdout())
		},
	}
}

// execSecretList implements "secret list". Separated for testability.
func execSecretList(ctx context.Context, s secrets.Store, out io.Writer) error {
	metas, err := s.List(ctx)
	if err != nil {
		return fmt.Errorf("secret list: %w", err)
	}

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
		Args:  exactArgs(1),
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
	_ = signalFn(paths.PIDFile())
	return nil
}
