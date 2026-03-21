package cmd

import "github.com/spf13/cobra"

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create, start, and provision the sandbox",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return svc.Create()
	},
}

func init() {
	rootCmd.AddCommand(createCmd)
}
