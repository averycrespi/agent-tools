package main

import (
	"fmt"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/auth"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(tokenCmd)
}

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Print the current auth token (creates one if absent)",
	RunE: func(_ *cobra.Command, _ []string) error {
		tok, err := auth.EnsureToken(auth.TokenPath())
		if err != nil {
			return fmt.Errorf("ensuring auth token: %w", err)
		}
		fmt.Println(tok)
		return nil
	},
}
