package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/http-broker/internal/config"
	"github.com/averycrespi/agent-tools/http-broker/internal/rules"
)

var rulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "Inspect and validate rules.json",
}

var rulesPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the effective rules file path",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(configPath())
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), config.EffectiveRulesPath(configPath(), cfg))
		return nil
	},
}

var rulesShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the effective rules document",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(configPath())
		if err != nil {
			return err
		}
		_, doc, err := config.LoadRulesForConfig(configPath(), cfg)
		if err != nil {
			return err
		}
		out, err := marshalIndent(doc)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	},
}

var rulesCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Validate rules.json without restarting the proxy",
	Long: "Compiles the rules document and reports the first validation failure.\n" +
		"Run this before sending SIGHUP: an invalid file leaves the previous\n" +
		"ruleset serving traffic, so a silent typo is easy to miss.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(configPath())
		if err != nil {
			return err
		}
		// Go through LoadRulesForConfig, as `path` and `show` do, so a fresh
		// install validates the generated default document rather than failing
		// with a bare "no such file" — this is the first command an operator
		// runs, and its own help text points them at it.
		path, doc, err := config.LoadRulesForConfig(configPath(), cfg)
		if err != nil {
			return err
		}
		engine, err := rules.New(doc)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: ok (%d rules, fallthrough %q)\n",
			path, len(engine.RuleNames()), engine.Fallthrough())
		return nil
	},
}

var rulesRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Validate rules.json and rewrite it in canonical form",
	Long: "Loads the rules document, validates it, and writes it back formatted\n" +
		"consistently. Creates the default document if none exists.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		written, err := config.RefreshRules(configPath())
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", written)
		return nil
	},
}

func init() {
	rulesCmd.AddCommand(rulesPathCmd, rulesShowCmd, rulesCheckCmd, rulesRefreshCmd)
	rootCmd.AddCommand(rulesCmd)
}

// marshalIndent renders a value as the same indented JSON the config and rules
// files use, so `show` output can be pasted straight back into a file.
func marshalIndent(v any) ([]byte, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rendering JSON: %w", err)
	}
	return out, nil
}
