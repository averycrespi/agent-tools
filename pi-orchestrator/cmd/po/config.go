package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/config"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage po configuration",
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print config file path",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), config.ConfigFilePath())
		return err
	},
}

var configRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Create or refresh config defaults",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return config.Refresh()
	},
}

var configEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Open config in $EDITOR",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		if err := config.Refresh(); err != nil {
			return err
		}
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		ecmd := exec.Command(editor, config.ConfigFilePath()) //nolint:gosec // user-selected editor is intentional.
		ecmd.Stdin = os.Stdin
		ecmd.Stdout = os.Stdout
		ecmd.Stderr = os.Stderr
		return ecmd.Run()
	},
}

func init() {
	configCmd.AddCommand(configPathCmd, configRefreshCmd, configEditCmd)
}
