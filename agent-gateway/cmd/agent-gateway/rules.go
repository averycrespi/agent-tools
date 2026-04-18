package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/daemon"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/secrets"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/store"
)

// secretsLister lists known secret names. The staticSecretsLister
// implementation is used by tests; production wiring queries the live
// state DB (see loadKnownSecrets).
type secretsLister interface {
	List() []string
}

// staticSecretsLister returns a fixed set of secret names. Production wiring
// builds one from the live state DB; tests construct one directly.
type staticSecretsLister struct {
	names []string
}

func (s *staticSecretsLister) List() []string { return s.names }

// loadKnownSecrets opens the state DB and reads the set of known secret
// names without requiring the master key. Returns an empty lister and logs
// a warning on stderr if the DB is unavailable — the check then proceeds
// and every ${secrets.X} reference becomes a warning (fail-open).
func loadKnownSecrets(errOut io.Writer) secretsLister {
	db, err := store.Open(paths.StateDB())
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "warning: could not open state db: %v\n", err)
		return &staticSecretsLister{}
	}
	defer func() { _ = db.Close() }()

	names, err := secrets.ListNames(context.Background(), db)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "warning: could not list secret names: %v\n", err)
		return &staticSecretsLister{}
	}
	return &staticSecretsLister{names: names}
}

// secretsRefRE matches ${secrets.<name>} in inject set_header values.
// It intentionally does NOT match ${agent.name} or ${agent.id}.
var secretsRefRE = regexp.MustCompile(`\$\{secrets\.([A-Za-z_][A-Za-z0-9_]*)\}`)

func newRulesCmd() *cobra.Command {
	rulesCmd := &cobra.Command{
		Use:   "rules",
		Short: "Manage agent-gateway rules",
	}
	rulesCmd.AddCommand(newRulesCheckCmd())
	rulesCmd.AddCommand(newRulesReloadCmd())
	return rulesCmd
}

// newRulesReloadCmd returns a cobra.Command for "rules reload".
func newRulesReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Signal the running daemon to reload rules",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return execRulesReload(
				cmd,
				paths.PIDFile(),
				daemon.DefaultVerifyComm,
				daemon.DefaultSendSignal,
				cmd.OutOrStdout(),
			)
		},
	}
}

// execRulesReload sends SIGHUP to the daemon identified by the PID file at
// pidPath. verify and send are injectable for tests. Output is written to out.
// If no PID file exists the function prints "no daemon running" and returns nil.
func execRulesReload(
	_ interface{},
	pidPath string,
	verify func(pid int) (bool, error),
	send func(pid int, sig os.Signal) error,
	out io.Writer,
) error {
	err := daemon.SignalDaemonWithVerifier(pidPath, verify, send)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_, _ = fmt.Fprintln(out, "no daemon running")
			return nil
		}
		return fmt.Errorf("rules reload: %w", err)
	}
	_, _ = fmt.Fprintln(out, "reloaded")
	return nil
}

func newRulesCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Validate rules files for syntax errors and undefined secrets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			lister := loadKnownSecrets(cmd.ErrOrStderr())
			return execRulesCheck(cmd, paths.RulesDir(), lister)
		},
	}
}

// execRulesCheck parses the rules directory, reports parse errors and missing
// secret warnings, and returns a non-nil error only on parse failure.
func execRulesCheck(cmd *cobra.Command, dir string, lister secretsLister) error {
	// Count files for the "ok" line.
	hclFiles, err := filepath.Glob(filepath.Join(dir, "*.hcl"))
	if err != nil {
		return fmt.Errorf("rules check: glob %q: %w", dir, err)
	}
	fileCount := len(hclFiles)

	// Parse all rules.
	parsed, warnings, err := rules.ParseDir(dir)
	if err != nil {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "error:", err)
		return err
	}

	// Print any parse-time warnings (unrecognised template syntax).
	for _, w := range warnings {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "warning:", w)
	}

	// Semantic check: cross-reference ${secrets.X} against the known secrets.
	knownSecrets := make(map[string]struct{})
	for _, name := range lister.List() {
		knownSecrets[name] = struct{}{}
	}

	for _, rule := range parsed {
		if rule.Inject == nil {
			continue
		}
		for _, val := range rule.Inject.SetHeaders {
			matches := secretsRefRE.FindAllStringSubmatch(val, -1)
			for _, m := range matches {
				secretName := m[1]
				if _, ok := knownSecrets[secretName]; !ok {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(),
						"warning: rule %q references undefined secret %q\n",
						rule.Name, secretName,
					)
				}
			}
		}
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(),
		"ok: %d rules parsed from %d files\n",
		len(parsed), fileCount,
	)

	return nil
}
