package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/http-broker/internal/ca"
	"github.com/averycrespi/agent-tools/http-broker/internal/paths"
)

var caCmd = &cobra.Command{
	Use:   "ca",
	Short: "Manage the local certificate authority used for interception",
}

var caExportOut string

var caExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Write the CA certificate to stdout or a file",
	Long: "Prints the PEM-encoded CA certificate.\n\n" +
		"Sandboxes receive this file through sandbox-manager's copy_paths, not by\n" +
		"fetching it over the network. The private key is never exported.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		authority, err := ca.LoadOrGenerate(paths.CAKey(), paths.CACert())
		if err != nil {
			return err
		}

		if caExportOut == "" {
			if _, err := cmd.OutOrStdout().Write(authority.RootPEM()); err != nil {
				return fmt.Errorf("writing certificate to stdout: %w", err)
			}
			return nil
		}
		if err := os.WriteFile(caExportOut, authority.RootPEM(), 0o644); err != nil { //nolint:gosec // the CA certificate is public by design
			return fmt.Errorf("writing %s: %w", caExportOut, err)
		}
		cmd.Printf("wrote %s\n", caExportOut)
		return nil
	},
}

var caRotateConfirm bool

var caRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Generate a new CA, invalidating every provisioned sandbox",
	Long: "Generates a new root CA and replaces ca.key and ca.pem.\n\n" +
		"There is no overlap window. Every sandbox that trusts the old CA stops\n" +
		"trusting this proxy the moment rotation completes, and TLS interception\n" +
		"fails there until provisioning is re-run in each one. Rotate when the key\n" +
		"may have leaked, not as routine maintenance.\n\n" +
		"A running `serve` picks up the new CA on SIGHUP.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if !caRotateConfirm {
			return fmt.Errorf("refusing to rotate without --yes: every provisioned sandbox will stop trusting this proxy until provisioning is re-run there")
		}

		authority, err := ca.LoadOrGenerate(paths.CAKey(), paths.CACert())
		if err != nil {
			return err
		}
		if err := authority.Rotate(); err != nil {
			return err
		}

		cmd.Printf("rotated CA: %s\n", paths.CACert())
		cmd.Println("re-run provisioning in every sandbox, then send SIGHUP to a running serve")
		return nil
	},
}

var caPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the CA certificate path",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cmd.Println(paths.CACert())
		return nil
	},
}

func init() {
	caExportCmd.Flags().StringVarP(&caExportOut, "out", "o", "", "write the certificate to this file instead of stdout")
	caRotateCmd.Flags().BoolVar(&caRotateConfirm, "yes", false, "confirm that every provisioned sandbox will need re-provisioning")

	caCmd.AddCommand(caExportCmd, caRotateCmd, caPathCmd)
	rootCmd.AddCommand(caCmd)
}
