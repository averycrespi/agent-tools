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

// loadKnownSecrets opens the state DB read-only and reads the set of known
// secret names without requiring the master key or creating the DB.
// Returns an empty lister if the DB is absent (fail-open: every
// ${secrets.X} reference becomes a warning) or on any other open error.
func loadKnownSecrets(errOut io.Writer) secretsLister {
	db, err := store.OpenReadOnly(paths.StateDB())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			_, _ = fmt.Fprintf(errOut, "warning: could not open state db: %v\n", err)
		}
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

// secretsRefRE matches ${secrets.<name>} in inject replace_header values.
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
	var strict bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate rules files for syntax errors and undefined secrets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			lister := loadKnownSecrets(cmd.ErrOrStderr())
			return execRulesCheck(cmd, paths.RulesDir(), lister, strict)
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false, "Exit non-zero if any warnings are emitted")
	return cmd
}

// execRulesCheck parses the rules directory, reports parse errors, missing
// secret warnings, and coverage warnings. Returns a non-nil error on parse
// failure, or (when strict=true) if any warnings were emitted.
func execRulesCheck(cmd *cobra.Command, dir string, lister secretsLister, strict bool) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	out := cmd.OutOrStdout()

	// Count files for the "ok" line.
	hclFiles, err := filepath.Glob(filepath.Join(dir, "*.hcl"))
	if err != nil {
		return fmt.Errorf("rules check: glob %q: %w", dir, err)
	}
	fileCount := len(hclFiles)

	// Parse all rules.
	parsed, parseWarnings, err := rules.ParseDir(dir)
	if err != nil {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "error:", err)
		return err
	}

	var totalWarnings int

	// Print any parse-time warnings (unrecognised template syntax).
	for _, w := range parseWarnings {
		_, _ = fmt.Fprintln(out, "warning:", w)
		totalWarnings++
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
		for _, val := range rule.Inject.ReplaceHeaders {
			matches := secretsRefRE.FindAllStringSubmatch(val, -1)
			for _, m := range matches {
				secretName := m[1]
				if _, ok := knownSecrets[secretName]; !ok {
					_, _ = fmt.Fprintf(out,
						"warning: rule %q references undefined secret %q\n",
						rule.Name, secretName,
					)
					totalWarnings++
				}
			}
		}
	}

	// Coverage check: open state DB read-only and run warnSecretCoverage.
	dbPath := paths.StateDB()
	db, dbErr := store.OpenReadOnly(dbPath)
	switch {
	case errors.Is(dbErr, os.ErrNotExist):
		_, _ = fmt.Fprintln(out, "note: state DB not found; skipping secret coverage check")
	case dbErr != nil:
		return fmt.Errorf("rules check: open state db: %w", dbErr)
	default:
		defer func() { _ = db.Close() }()
		secretsStore, err := secrets.OpenForRead(db, nil)
		if err != nil {
			return fmt.Errorf("rules check: load secrets: %w", err)
		}
		// Build a throwaway engine from the same dir to drive warnSecretCoverage.
		// ParseDir already succeeded above so this should not fail.
		engine, err := rules.NewEngine(dir)
		if err != nil {
			return fmt.Errorf("rules check: build engine: %w", err)
		}
		for _, w := range warnSecretCoverage(ctx, engine, secretsStore) {
			_, _ = fmt.Fprintln(out, "warning:", w)
			totalWarnings++
		}
	}

	_, _ = fmt.Fprintf(out,
		"ok: %d rules parsed from %d files\n",
		len(parsed), fileCount,
	)

	if strict && totalWarnings > 0 {
		return fmt.Errorf("rules check: %d warning(s) (--strict)", totalWarnings)
	}
	return nil
}
