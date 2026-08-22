package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/spf13/cobra"
)

type commandFailure struct{}

func (commandFailure) Error() string { return "command failed" }

func newRootCmd() *cobra.Command {
	command := &cobra.Command{
		Use:           "mcp-gateway",
		Short:         "Locally secure, deny-by-default MCP gateway",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	command.AddCommand(newRestoreCmd(storage.VerifyCurrent))
	return command
}

func newRestoreCmd(verifyCurrent func(context.Context, string) (storage.Identity, error)) *cobra.Command {
	var dataDir string
	var verify bool
	command := &cobra.Command{
		Use:   "restore",
		Short: "Verify or restore a stopped Gateway database",
		Args: func(command *cobra.Command, args []string) error {
			if len(args) != 0 {
				return writeCommandFailure(command, "restore", "invalid_command")
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			if !verify || dataDir == "" {
				return writeCommandFailure(command, "restore", "invalid_command")
			}
			identity, err := verifyCurrent(command.Context(), dataDir)
			if err != nil {
				code := "storage_unavailable"
				if errors.Is(err, gatewaypaths.ErrInUse) {
					code = "gateway_running"
				}
				return writeCommandFailure(command, "restore", code)
			}
			result := struct {
				OK             bool   `json:"ok"`
				Operation      string `json:"operation"`
				Mode           string `json:"mode"`
				InstallationID string `json:"installation_id"`
				Revision       string `json:"revision"`
			}{
				OK:             true,
				Operation:      "restore",
				Mode:           "verify_current",
				InstallationID: identity.InstallationID,
				Revision:       fmt.Sprintf("%d", identity.Revision),
			}
			if err := json.NewEncoder(command.OutOrStdout()).Encode(result); err != nil {
				return commandFailure{}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&verify, "verify-current", false, "verify and clear a stopped installation's storage latch")
	command.Flags().StringVar(&dataDir, "data-dir", "", "owner-only Gateway data directory")
	command.SetFlagErrorFunc(func(command *cobra.Command, _ error) error {
		return writeCommandFailure(command, "restore", "invalid_command")
	})
	return command
}

func writeCommandFailure(command *cobra.Command, operation, code string) error {
	result := struct {
		OK        bool   `json:"ok"`
		Operation string `json:"operation"`
		Code      string `json:"code"`
	}{OK: false, Operation: operation, Code: code}
	if err := json.NewEncoder(command.OutOrStdout()).Encode(result); err != nil {
		return commandFailure{}
	}
	return commandFailure{}
}
