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
		"Transfer this certificate to clients through a trusted channel, not by\n" +
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
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", caExportOut)
		return nil
	},
}

var caRotateConfirm bool

var caRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Generate a new CA, invalidating every client trusting the old CA",
	Long: "Generates a new root CA and replaces ca.key and ca.pem.\n\n" +
		"There is no overlap window. Clients trusting only the old CA cannot verify\n" +
		"new interception certificates once a running serve reloads the CA.\n" +
		"Manually install the new public CA in each client trust store. Rotate when the key\n" +
		"may have leaked, not as routine maintenance.\n\n" +
		"A running `serve` picks up the new CA on SIGHUP.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if !caRotateConfirm {
			return fmt.Errorf("refusing to rotate without --yes: every client trusting the old CA will stop trusting this proxy until the new CA is manually installed there")
		}

		authority, err := ca.LoadOrGenerate(paths.CAKey(), paths.CACert())
		if err != nil {
			return err
		}
		if err := authority.Rotate(); err != nil {
			return err
		}

		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "rotated CA: %s\n", paths.CACert())
		cmd.PrintErrln("securely transfer and install the new CA in every client trust store, then send SIGHUP to a running serve")
		return nil
	},
}

var caPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the CA certificate path",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), paths.CACert())
		return nil
	},
}

func init() {
	caExportCmd.Flags().StringVarP(&caExportOut, "out", "o", "", "write the certificate to this file instead of stdout")
	caRotateCmd.Flags().BoolVar(&caRotateConfirm, "yes", false, "confirm that every client trusting the old CA will need the new CA installed")

	caCmd.AddCommand(caExportCmd, caRotateCmd, caPathCmd)
	rootCmd.AddCommand(caCmd)
}
