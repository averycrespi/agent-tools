package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var supervisorRunID string

var supervisorCmd = &cobra.Command{
	Use:    "supervisor",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		if supervisorRunID == "" {
			return fmt.Errorf("--run-id is required")
		}
		return nil
	},
}

func init() {
	supervisorCmd.Flags().StringVar(&supervisorRunID, "run-id", "", "workflow run id")
}
