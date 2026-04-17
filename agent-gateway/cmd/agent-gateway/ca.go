package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/ca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/daemon"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

func newCACmd() *cobra.Command {
	caCmd := &cobra.Command{
		Use:   "ca",
		Short: "Manage the local root CA",
	}

	caCmd.AddCommand(newCAExportCmd())
	caCmd.AddCommand(newCARotateCmd())

	return caCmd
}

func newCAExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export",
		Short: "Print the root CA certificate (PEM) to stdout",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			certPath := paths.CACert()
			data, err := os.ReadFile(certPath)
			if err != nil {
				return fmt.Errorf("read CA cert: %w", err)
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
}

func newCARotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate",
		Short: "Rotate the root CA (regenerate and replace on disk)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			authority, err := ca.LoadOrGenerate(paths.CAKey(), paths.CACert())
			if err != nil {
				return fmt.Errorf("load CA: %w", err)
			}

			if err := authority.Rotate(); err != nil {
				return fmt.Errorf("rotate CA: %w", err)
			}

			// Signal the daemon to reload; tolerate the case where no daemon is running.
			if err := daemon.SignalDaemon(paths.PIDFile()); err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("signal daemon: %w", err)
				}
				// Daemon is not running — that's fine.
			}

			certPath := paths.CACert()
			_, err = fmt.Fprintf(cmd.OutOrStdout(),
				"rotated: %s \u2014 every sandbox must re-trust.\n",
				certPath,
			)
			return err
		},
	}
}
