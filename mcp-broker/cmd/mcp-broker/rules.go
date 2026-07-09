package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

var rulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "Manage mcp-broker policy rules",
}

var rulesPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the rules file path",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		path, err := rulesFilePath()
		if err != nil {
			return err
		}
		fmt.Println(path)
		return nil
	},
}

var rulesRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Refresh rules file with current defaults",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		result, err := refreshRulesFileWithResult()
		if err != nil {
			return err
		}
		warnRulesLoadResult(result)
		fmt.Println(result.Path)
		return nil
	},
}

var rulesEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Open the rules file in your editor",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		result, err := refreshRulesFileWithResult()
		if err != nil {
			return err
		}
		warnRulesLoadResult(result)
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		c := exec.Command(editor, result.Path) //nolint:gosec // editor is user-controlled via $EDITOR
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}

func init() {
	rulesCmd.AddCommand(rulesPathCmd)
	rulesCmd.AddCommand(rulesRefreshCmd)
	rulesCmd.AddCommand(rulesEditCmd)
	rootCmd.AddCommand(rulesCmd)
}

func rulesFilePath() (string, error) {
	return config.ResolveRulesPath(configPath())
}

func refreshRulesFile() (string, error) {
	result, err := refreshRulesFileWithResult()
	if err != nil {
		return "", err
	}
	return result.Path, nil
}

func refreshRulesFileWithResult() (config.RulesLoadResult, error) {
	return config.RefreshRulesWithResult(configPath())
}

func warnRulesLoadResult(result config.RulesLoadResult) {
	if result.IgnoredLegacy {
		fmt.Fprintf(os.Stderr, "warning: legacy config rules ignored because rules file exists: %s\n", result.Path)
	}
}
