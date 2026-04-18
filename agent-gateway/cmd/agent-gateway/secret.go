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

// stdoutIsTTY is a package-level variable for testing stdout TTY detection.
var stdoutIsTTY = func() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
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
	// Stale PID, wrong comm, etc. — all tolerated per Task 9 convention.
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

	secretCmd.AddCommand(newSecretSetCmd())
	secretCmd.AddCommand(newSecretListCmd())
	secretCmd.AddCommand(newSecretRotateCmd())
	secretCmd.AddCommand(newSecretRMCmd())
	secretCmd.AddCommand(newSecretExportCmd())
	secretCmd.AddCommand(newSecretMasterCmd())

	return secretCmd
}

// newSecretSetCmd returns the "secret set <name>" command.
func newSecretSetCmd() *cobra.Command {
	var (
		agent string
		desc  string
	)
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Store a secret value (reads from stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, cleanup, err := openSecretStore()
			if err != nil {
				return err
			}
			defer cleanup()

			return execSecretSet(
				cmd.Context(),
				s,
				args[0],
				agent,
				desc,
				cmd.InOrStdin(),
				cmd.OutOrStdout(),
				stdinIsTTY(),
				func(pidPath string) error { return sendHUP(pidPath) },
			)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent name (omit for global scope)")
	cmd.Flags().StringVar(&desc, "desc", "", "human-readable description")
	return cmd
}

// execSecretSet implements "secret set". Separated for testability.
// signalFn receives the PID file path and is responsible for sending SIGHUP.
func execSecretSet(
	ctx context.Context,
	s secrets.Store,
	name, agent, desc string,
	in io.Reader,
	out io.Writer,
	isTTY bool,
	signalFn func(string) error,
) error {
	value, err := readStdinValue(in, isTTY)
	if err != nil {
		return err
	}

	if err := s.Set(ctx, name, agent, value, desc); err != nil {
		return fmt.Errorf("secret set: %w", err)
	}

	// Shadow warning: if agent-scoped, check whether a global row also exists.
	if agent != "" {
		_, _, globalErr := s.Get(ctx, name, "")
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
	_, _ = fmt.Fprintln(w, "NAME\tSCOPE\tCREATED\tROTATED\tLAST-USED\tDESCRIPTION")
	for _, m := range metas {
		lastUsed := "-"
		if m.LastUsedAt != nil {
			lastUsed = m.LastUsedAt.UTC().Format(time.RFC3339)
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			m.Name,
			m.Scope,
			m.CreatedAt.UTC().Format(time.RFC3339),
			m.RotatedAt.UTC().Format(time.RFC3339),
			lastUsed,
			m.Description,
		)
	}
	return w.Flush()
}

// newSecretRotateCmd returns the "secret rotate <name>" command.
func newSecretRotateCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "rotate <name>",
		Short: "Update the value of an existing secret (reads new value from stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, cleanup, err := openSecretStore()
			if err != nil {
				return err
			}
			defer cleanup()
			return execSecretRotate(
				cmd.Context(),
				s,
				args[0],
				agent,
				cmd.InOrStdin(),
				cmd.OutOrStdout(),
				stdinIsTTY(),
				func(pidPath string) error { return sendHUP(pidPath) },
			)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent name (omit for global scope)")
	return cmd
}

// execSecretRotate implements "secret rotate". Separated for testability.
func execSecretRotate(
	ctx context.Context,
	s secrets.Store,
	name, agent string,
	in io.Reader,
	out io.Writer,
	isTTY bool,
	signalFn func(string) error,
) error {
	newValue, err := readStdinValue(in, isTTY)
	if err != nil {
		return err
	}

	if err := s.Rotate(ctx, name, agent, newValue); err != nil {
		return fmt.Errorf("secret rotate: %w", err)
	}

	_, _ = fmt.Fprintf(out, "rotated: %s\n", name)
	_ = signalFn(paths.PIDFile())
	return nil
}

// newSecretRMCmd returns the "secret rm <name>" command.
func newSecretRMCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Delete a secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, cleanup, err := openSecretStore()
			if err != nil {
				return err
			}
			defer cleanup()
			return execSecretRM(
				cmd.Context(),
				s,
				args[0],
				agent,
				cmd.OutOrStdout(),
				func(pidPath string) error { return sendHUP(pidPath) },
			)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent name (omit for global scope)")
	return cmd
}

// execSecretRM implements "secret rm". Separated for testability.
func execSecretRM(
	ctx context.Context,
	s secrets.Store,
	name, agent string,
	out io.Writer,
	signalFn func(string) error,
) error {
	if err := s.Delete(ctx, name, agent); err != nil {
		return fmt.Errorf("secret rm: %w", err)
	}
	_, _ = fmt.Fprintf(out, "deleted: %s\n", name)
	_ = signalFn(paths.PIDFile())
	return nil
}

// newSecretExportCmd returns the "secret export <name>" command.
func newSecretExportCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "export <name>",
		Short: "Print the raw secret value to stdout (refuses when stdout is a TTY)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, cleanup, err := openSecretStore()
			if err != nil {
				return err
			}
			defer cleanup()
			return execSecretExport(
				cmd.Context(),
				s,
				args[0],
				agent,
				cmd.OutOrStdout(),
				stdoutIsTTY(),
			)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent name (omit for global scope)")
	return cmd
}

// execSecretExport implements "secret export". Separated for testability.
func execSecretExport(
	ctx context.Context,
	s secrets.Store,
	name, agent string,
	out io.Writer,
	isTTY bool,
) error {
	if isTTY {
		return fmt.Errorf("must pipe output (stdout is a TTY)")
	}

	value, _, err := s.Get(ctx, name, agent)
	if err != nil {
		return fmt.Errorf("secret export: %w", err)
	}

	// Write raw bytes, no trailing newline.
	_, err = io.WriteString(out, value)
	return err
}

// newSecretMasterCmd returns the "secret master" command group.
func newSecretMasterCmd() *cobra.Command {
	masterCmd := &cobra.Command{
		Use:   "master",
		Short: "Master key operations",
	}
	masterCmd.AddCommand(newSecretMasterRotateCmd())
	return masterCmd
}

// newSecretMasterRotateCmd returns the "secret master rotate" command.
func newSecretMasterRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate",
		Short: "Re-encrypt all secrets under a new master key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cleanup, err := openSecretStore()
			if err != nil {
				return err
			}
			defer cleanup()
			return execSecretMasterRotate(
				cmd.Context(),
				s,
				cmd.OutOrStdout(),
				func(pidPath string) error { return sendHUP(pidPath) },
			)
		},
	}
}

// execSecretMasterRotate implements "secret master rotate". Separated for testability.
func execSecretMasterRotate(
	ctx context.Context,
	s secrets.Store,
	out io.Writer,
	signalFn func(string) error,
) error {
	if err := s.MasterRotate(ctx); err != nil {
		return fmt.Errorf("secret master rotate: %w", err)
	}
	_, _ = fmt.Fprintln(out, "rotated master key")
	_ = signalFn(paths.PIDFile())
	return nil
}
