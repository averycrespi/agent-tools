package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/daemon"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/dashboard"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

func newTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage admin tokens",
	}
	cmd.AddCommand(newTokenRotateCmd())
	return cmd
}

func newTokenRotateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rotate",
		Short: "Rotate an admin token",
	}
	cmd.AddCommand(newTokenRotateAdminCmd())
	return cmd
}

func newTokenRotateAdminCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Rotate the admin dashboard token and reload the running daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			confirmFn := func() (bool, error) {
				return confirm(cmd.InOrStdin(), cmd.OutOrStdout(), stdinIsTTY(), force,
					"Rotate the admin dashboard token? Existing dashboard sessions will be invalidated.")
			}
			return execTokenRotateAdmin(
				paths.AdminTokenFile(),
				paths.PIDFile(),
				daemon.DefaultVerifyComm,
				daemon.DefaultSendSignal,
				cmd.OutOrStdout(),
				confirmFn,
			)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompt")
	return cmd
}

// execTokenRotateAdmin generates a new admin token, writes it to tokenPath,
// and sends SIGHUP to the running daemon so it reloads the token in memory.
// If no daemon is running the token is still rotated (so the next startup
// picks up the new value). Output is written to out. verify and send are
// injectable for tests.
func execTokenRotateAdmin(
	tokenPath string,
	pidPath string,
	verify func(pid int) (bool, error),
	send func(pid int, sig os.Signal) error,
	out io.Writer,
	confirmFn func() (bool, error),
) error {
	ok, err := confirmFn()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	tok, err := dashboard.GenerateAdminToken(tokenPath)
	if err != nil {
		return fmt.Errorf("token rotate admin: %w", err)
	}
	_, _ = fmt.Fprintln(out, "rotated admin token:", tok)

	// Best-effort: signal the running daemon. If there is no daemon,
	// or the PID file is stale, report but do not fail.
	if err := daemon.SignalDaemonWithVerifier(pidPath, verify, send); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_, _ = fmt.Fprintln(out, "no daemon running; token will take effect on next start")
		} else {
			_, _ = fmt.Fprintf(out, "warning: could not signal daemon: %v\n", err)
		}
	} else {
		_, _ = fmt.Fprintln(out, "daemon signalled")
	}

	return nil
}
