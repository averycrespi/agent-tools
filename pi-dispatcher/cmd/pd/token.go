package main

import (
	"fmt"

	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/auth"
	"github.com/spf13/cobra"
)

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage the pd auth token",
}

var tokenRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Rotate the pd auth token",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		path := auth.TokenPath()
		if _, err := auth.RotateToken(path); err != nil {
			return fmt.Errorf("rotating auth token: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "New auth token written to %s\n", path)
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Restart running pd dashboard servers to apply.")
		return nil
	},
}

func init() {
	tokenCmd.AddCommand(tokenRotateCmd)
}
