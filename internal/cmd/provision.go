package cmd

import "github.com/spf13/cobra"

var provisionCmd = &cobra.Command{
	Use:   "provision",
	Short: "Re-provision the sandbox (copy files and run scripts)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return svc.Provision()
	},
}

func init() {
	rootCmd.AddCommand(provisionCmd)
}
