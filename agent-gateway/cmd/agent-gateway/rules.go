package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
)

// secretsLister lists known secret names. An empty slice means no secrets
// store is available (Task 22 will wire the real implementation).
type secretsLister interface {
	List() []string
}

// staticSecretsLister is a stub that returns a fixed set of secret names.
// Used when --secrets-list is provided (and for tests before Task 22).
type staticSecretsLister struct {
	names []string
}

func (s *staticSecretsLister) List() []string { return s.names }

// secretsRefRE matches ${secrets.<name>} in inject set_header values.
// It intentionally does NOT match ${agent.name} or ${agent.id}.
var secretsRefRE = regexp.MustCompile(`\$\{secrets\.([A-Za-z_][A-Za-z0-9_]*)\}`)

func newRulesCmd() *cobra.Command {
	rulesCmd := &cobra.Command{
		Use:   "rules",
		Short: "Manage agent-gateway rules",
	}
	rulesCmd.AddCommand(newRulesCheckCmd())
	return rulesCmd
}

func newRulesCheckCmd() *cobra.Command {
	var (
		rulesDir    string
		secretsList string
	)

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate rules files for syntax errors and undefined secrets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := rulesDir
			if dir == "" {
				dir = paths.RulesDir()
			}

			// Build the secrets lister from --secrets-list flag.
			var lister secretsLister
			if secretsList != "" {
				parts := strings.Split(secretsList, ",")
				// Trim whitespace around each name.
				names := make([]string, 0, len(parts))
				for _, p := range parts {
					if s := strings.TrimSpace(p); s != "" {
						names = append(names, s)
					}
				}
				lister = &staticSecretsLister{names: names}
			} else {
				// No secrets list provided — stub that returns empty (all refs warn).
				lister = &staticSecretsLister{names: nil}
			}

			return execRulesCheck(cmd, dir, lister)
		},
	}

	cmd.Flags().StringVar(&rulesDir, "rules-dir", "", fmt.Sprintf("rules directory (default %q)", paths.RulesDir()))
	cmd.Flags().StringVar(&secretsList, "secrets-list", "", "comma-separated list of known secret names (bypasses secrets store)")

	return cmd
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
