package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/daemon"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/store"
)

func newReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Signal the running daemon to reload rules, agents, secrets, admin token, and CA",
		Long: `Sends SIGHUP to the daemon to re-read all mutable state:
  - Rule files in rules.d/
  - Agent registry (tokens)
  - Secret value cache (re-decrypts on next use)
  - Admin token file
  - CA certificate (invalidates leaf cache)

Does NOT reload config.hcl. Edits to config.hcl require a restart.

Exits non-zero if no daemon is running.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return execReload(cmd, paths.PIDFile(),
				paths.StateDB(), paths.ConfigFile(),
				daemon.DefaultVerifyComm, daemon.DefaultSendSignal,
				cmd.OutOrStdout())
		},
	}
}

// execReload sends SIGHUP to the daemon identified by the PID file at pidPath.
// dbPath and cfgPath are used to compare the recorded config hash against the
// current file — if they differ a warning is printed to out. The check is
// best-effort: all errors are silently ignored so the SIGHUP always fires.
// verify and send are injectable for tests. Output is written to out.
// Returns a non-nil error (containing "no daemon running") if the PID file is absent.
func execReload(
	_ interface{},
	pidPath string,
	dbPath string,
	cfgPath string,
	verify func(pid int) (bool, error),
	send func(pid int, sig os.Signal) error,
	out io.Writer,
) error {
	// Best-effort: warn if config.hcl has changed since the daemon started.
	// All errors in this path are silently ignored — the SIGHUP still fires.
	if dbPath != "" && cfgPath != "" {
		if db, err := store.OpenReadOnly(dbPath); err == nil {
			defer func() { _ = db.Close() }()
			if recorded, _ := store.GetMeta(db, "config_hash"); recorded != "" {
				if current, err := sha256File(cfgPath); err == nil && current != recorded {
					_, _ = fmt.Fprintf(out,
						"warning: config.hcl has changed since the daemon started.\n"+
							"  Changes to config.hcl require a daemon restart.\n"+
							"  Apply with: kill $(cat %s) and re-run 'agent-gateway serve'.\n",
						pidPath,
					)
				}
			}
		}
	}

	err := daemon.SignalDaemonWithVerifier(pidPath, verify, send)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no daemon running; start it with 'agent-gateway serve'")
		}
		return fmt.Errorf("reload: %w", err)
	}
	_, _ = fmt.Fprintln(out, "reloaded")
	return nil
}
