package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/spf13/cobra"
)

type commandFailure struct{}

func (commandFailure) Error() string { return "command failed" }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type offlineDependencies struct {
	clock   admin.Clock
	entropy io.Reader
}

func newRootCmd() *cobra.Command {
	return newRootCmdWithDependencies(offlineDependencies{clock: systemClock{}, entropy: rand.Reader})
}

func newRootCmdWithDependencies(dependencies offlineDependencies) *cobra.Command {
	command := &cobra.Command{
		Use:           "mcp-gateway",
		Short:         "Locally secure, deny-by-default MCP gateway",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	command.AddCommand(
		newAdminAuthorityCmd("initialize", dependencies),
		newAdminAuthorityCmd("admin-reset", dependencies),
		newRestoreCmd(storage.VerifyCurrent),
	)
	return command
}

func newAdminAuthorityCmd(operation string, dependencies offlineDependencies) *cobra.Command {
	var dataDir string
	var secretOutput string
	command := &cobra.Command{
		Use:   operation,
		Short: "Create and publish Gateway admin authority",
		Args: func(command *cobra.Command, args []string) error {
			if len(args) != 0 {
				return writeCommandFailure(command, operation, "invalid_command")
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			if dataDir == "" {
				return writeCommandFailure(command, operation, "invalid_command")
			}
			sink := admin.NewTerminalSecretSink()
			if secretOutput != "" {
				sink = admin.NewFileSecretSink(secretOutput)
			}
			identity, err := executeAdminAuthority(command.Context(), operation, dataDir, sink, dependencies)
			if err != nil {
				return writeCommandFailure(command, operation, adminCommandErrorCode(err))
			}
			result := struct {
				OK             bool   `json:"ok"`
				Operation      string `json:"operation"`
				InstallationID string `json:"installation_id"`
				Revision       string `json:"revision"`
			}{
				OK:             true,
				Operation:      operation,
				InstallationID: identity.InstallationID,
				Revision:       fmt.Sprintf("%d", identity.Revision),
			}
			if err := json.NewEncoder(command.OutOrStdout()).Encode(result); err != nil {
				return commandFailure{}
			}
			return nil
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "owner-only Gateway data directory")
	command.Flags().StringVar(&secretOutput, "secret-output", "", "new owner-only file for the one-time admin bearer")
	command.SetFlagErrorFunc(func(command *cobra.Command, _ error) error {
		return writeCommandFailure(command, operation, "invalid_command")
	})
	return command
}

func executeAdminAuthority(
	ctx context.Context,
	operation string,
	dataDir string,
	sink admin.SecretSink,
	dependencies offlineDependencies,
) (storage.Identity, error) {
	ownership, err := gatewaypaths.AcquireForMaintenance(dataDir)
	if err != nil {
		return storage.Identity{}, fmt.Errorf("acquire stopped-process ownership: %w", err)
	}
	defer func() { _ = ownership.Close() }()

	var store *storage.Store
	if operation == "initialize" {
		installationID, idErr := admin.NewID(dependencies.clock.Now(), dependencies.entropy)
		if idErr != nil {
			return storage.Identity{}, idErr
		}
		store, err = storage.Initialize(ctx, ownership, installationID)
		if errors.Is(err, storage.ErrAlreadyInitialized) {
			store, err = storage.Open(ctx, ownership)
		}
	} else {
		store, err = storage.Open(ctx, ownership)
	}
	if err != nil {
		return storage.Identity{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = store.Close()
		}
	}()

	service := admin.NewService(store, dependencies.clock, dependencies.entropy)
	if operation == "initialize" {
		_, err = service.Initialize(ctx, sink)
	} else {
		_, err = service.Reset(ctx, sink)
	}
	if err != nil {
		return storage.Identity{}, err
	}
	identity, err := store.Identity(ctx)
	if err != nil {
		return storage.Identity{}, err
	}
	if err := store.Close(); err != nil {
		return storage.Identity{}, err
	}
	closed = true
	if err := ownership.MarkClean(); err != nil {
		return storage.Identity{}, err
	}
	return identity, nil
}

func adminCommandErrorCode(err error) string {
	switch {
	case errors.Is(err, gatewaypaths.ErrInUse):
		return "gateway_running"
	case errors.Is(err, admin.ErrAlreadyInitialized):
		return "already_initialized"
	case errors.Is(err, admin.ErrNotInitialized):
		return "not_initialized"
	case errors.Is(err, admin.ErrSecretPublication):
		return "secret_output_unavailable"
	default:
		return "storage_unavailable"
	}
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
