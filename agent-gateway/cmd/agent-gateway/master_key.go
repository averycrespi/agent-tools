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
		Short: "Rewrap the data-encryption key under a new master key",
		Long: `Rewrap the data-encryption key under a new master key.

Immediate consequences:
  - A new master key is generated and persisted to keychain or file fallback.
  - The data-encryption key (DEK) stored in the SQLite 'meta' table is
    re-wrapped under the new master key; secret row ciphertexts are unchanged.
  - The previous master key is deleted best-effort after the rotation succeeds.

Recovery:
  The new master key is persisted before the re-encryption transaction
  commits, so a failure mid-rotation leaves the new key on disk only as
  long as the deferred cleanup runs. If the process is killed between
  persisting the new key and committing, both master keys may remain on
  disk; in that case the active key id in the SQLite 'meta' table tells
  you which key is in use. Re-running 'master-key rotate' is safe — it
  retries the rewrap from the current state.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cleanup, err := openSecretStore()
			if err != nil {
				return err
			}
			defer cleanup()
			confirmFn := func() (bool, error) {
				return confirm(cmd.InOrStdin(), cmd.OutOrStdout(), stdinIsTTY(), force,
					"Rotate the master key? The data-encryption key will be rewrapped under a fresh master.")
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
