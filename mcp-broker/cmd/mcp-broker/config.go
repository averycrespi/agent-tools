package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage mcp-broker configuration",
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the config file path",
	Args:  cobra.NoArgs,
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Println(configPath())
	},
}

var configRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Refresh config file with current defaults",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		path, err := config.Refresh(configPath())
		if err != nil {
			return err
		}
		fmt.Println(path)
		return nil
	},
}

var configEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Open the config file in your editor",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		path, err := config.Refresh(configPath())
		if err != nil {
			return err
		}
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		c := exec.Command(editor, path) //nolint:gosec // editor is user-controlled via $EDITOR
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}

func init() {
	configCmd.AddCommand(configPathCmd)
	configCmd.AddCommand(configRefreshCmd)
	configCmd.AddCommand(configEditCmd)
	rootCmd.AddCommand(configCmd)
}
