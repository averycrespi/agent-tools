package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/egress-broker/internal/config"
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
		cmd.Println(config.EffectiveRulesPath(configPath(), cfg))
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
		cmd.Println(string(out))
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
		path := config.EffectiveRulesPath(configPath(), cfg)
		engine, err := config.LoadRulesEngine(path)
		if err != nil {
			return err
		}
		cmd.Println(fmt.Sprintf("%s: ok (%d rules, fallthrough %q)",
			path, len(engine.RuleNames()), engine.Fallthrough()))
		return nil
	},
}

func init() {
	rulesCmd.AddCommand(rulesPathCmd, rulesShowCmd, rulesCheckCmd)
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
