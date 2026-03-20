package cmd

import (
	"fmt"
	"os"
	osexec "os/exec"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/worktree-manager/internal/config"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage wt configuration",
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print config file path",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(config.ConfigFilePath())
	},
}

var configRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Create or refresh config file with latest defaults",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return config.Refresh(logger)
	},
}

var configEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Open config file in $EDITOR",
	Long:  "Open the config file in $EDITOR, creating default config if missing",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := config.Refresh(logger); err != nil {
			return err
		}

		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		path := config.ConfigFilePath()
		c := osexec.Command(editor, path) //nolint:gosec // editor is from trusted EDITOR env var
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}

func init() {
	configCmd.AddCommand(configPathCmd, configRefreshCmd, configEditCmd)
	rootCmd.AddCommand(configCmd)
}
