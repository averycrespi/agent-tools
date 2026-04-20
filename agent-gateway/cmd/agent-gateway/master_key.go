package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/secrets"
)

// newMasterKeyCmd builds the "master-key" command tree.
func newMasterKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "master-key",
		Short: "Manage the master key used to encrypt secrets",
	}
	cmd.AddCommand(newMasterKeyRotateCmd())
	return cmd
}

// newMasterKeyRotateCmd returns the "master-key rotate" command.
func newMasterKeyRotateCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "rotate",
		Short: "Re-encrypt all secrets under a new master key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cleanup, err := openSecretStore()
			if err != nil {
				return err
			}
			defer cleanup()
			confirmFn := func() (bool, error) {
				return confirm(cmd.InOrStdin(), cmd.OutOrStdout(), stdinIsTTY(), force,
					"Rotate the master key? Every secret will be re-encrypted.")
			}
			return execMasterKeyRotate(
				cmd.Context(),
				s,
				cmd.OutOrStdout(),
				confirmFn,
				func(pidPath string) error { return sendHUP(pidPath) },
			)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompt")
	return cmd
}

// execMasterKeyRotate implements "master-key rotate". Separated for testability.
func execMasterKeyRotate(
	ctx context.Context,
	s secrets.Store,
	out io.Writer,
	confirmFn func() (bool, error),
	signalFn func(string) error,
) error {
	ok, err := confirmFn()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if err := s.MasterRotate(ctx); err != nil {
		return fmt.Errorf("master-key rotate: %w", err)
	}
	_, _ = fmt.Fprintln(out, "rotated master key")
	_ = signalFn(paths.PIDFile())
	return nil
}
