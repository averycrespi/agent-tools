package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/config"
)

func newConfigCmd(configPath func() string) *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Manage agent-gateway configuration",
	}

	configCmd.AddCommand(newConfigPathCmd(configPath))
	configCmd.AddCommand(newConfigRefreshCmd(configPath))
	configCmd.AddCommand(newConfigEditCmd(configPath))

	return configCmd
}

func newConfigPathCmd(configPath func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), configPath())
		},
	}
}

func newConfigRefreshCmd(configPath func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Refresh config file with current defaults",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return config.Refresh(configPath())
		},
	}
}

func newConfigEditCmd(configPath func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the config file in your editor",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := config.Refresh(configPath()); err != nil {
				return err
			}
			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "vi"
			}
			c := exec.Command(editor, configPath()) //nolint:gosec // editor is user-controlled via $EDITOR
			c.Stdin = os.Stdin
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			return c.Run()
		},
	}
}
