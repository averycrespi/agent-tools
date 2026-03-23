package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/auth"
)

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage auth token",
}

var tokenRegenCmd = &cobra.Command{
	Use:   "regen",
	Short: "Generate a new auth token (invalidates existing clients and dashboard sessions)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		path := auth.TokenPath()

		// Delete existing token file so EnsureToken generates a new one.
		// Ignore error if file doesn't exist.
		_ = os.Remove(path)

		token, err := auth.EnsureToken(path)
		if err != nil {
			return fmt.Errorf("generating token: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "New token written to %s\n", path)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Restart the broker to apply. Update client configs with the new token.\n")
		// Don't print the token itself — user can cat the file.
		_ = token
		return nil
	},
}

func init() {
	tokenCmd.AddCommand(tokenRegenCmd)
	rootCmd.AddCommand(tokenCmd)
}
