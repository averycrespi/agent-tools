package cmd

import (
	"fmt"

	"github.com/averycrespi/agent-tools/sandbox-manager/internal/lima"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show sandbox status",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		status, err := svc.Status()
		if err != nil {
			return err
		}
		switch status {
		case lima.StatusRunning:
			fmt.Println("running")
		case lima.StatusStopped:
			fmt.Println("stopped")
		case lima.StatusNotCreated:
			fmt.Println("not created")
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
