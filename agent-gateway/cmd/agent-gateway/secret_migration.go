package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/composition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/spf13/cobra"
)

func newSecretMigrationCmd(operation string, dependencies offlineDependencies) *cobra.Command {
	var confirm, dryRun, jsonOutput, verified bool
	var installation string
	descriptions := map[string]string{"migrate-secrets": "Migrate native secrets to encrypted storage", "verify-secrets": "Verify encrypted secret completeness", "cleanup-native-secrets": "Delete verified retired native secrets"}
	action := map[string]string{ //nolint:gosec // Public maintenance descriptions, not secret material.
		"migrate-secrets":        "Copy and authenticate selected legacy secrets into encrypted custody; retain native sources and completed work on partial failure.",
		"verify-secrets":         "Authenticate all protected dependencies without native access or writes. Custody completeness does not prove operational readiness.",
		"cleanup-native-secrets": "Reconcile retained cleanup inventory and remove only retired installation-owned native items. Completed deletion is irreversible; uncertain effects remain recorded.",
	}[operation]
	usage := "agent-gateway maintenance " + operation
	if operation == "cleanup-native-secrets" {
		usage += " --operator-verified"
	}
	command := &cobra.Command{Use: operation, Short: descriptions[operation], Long: action + " Requires exclusive stopped ownership. Inspect --dry-run first. Migration never cleans up automatically. Before cleanup, run verify-secrets, perform operational checks, create a new backup and safeguard its separate master key. Legacy tooling removal requires separate human approval; binary downgrade safety is not promised.", Example: "  " + usage + " --dry-run\n  " + usage + " --confirm"}
	fail := func(message string) error {
		return writeOfflineProblem(command, offlineMode(jsonOutput), offlineUsageProblem(message, usage))
	}
	command.Args = func(_ *cobra.Command, args []string) error {
		if len(args) != 0 {
			return fail("Secret maintenance accepts no positional arguments.")
		}
		return nil
	}
	command.RunE = func(command *cobra.Command, _ []string) error {
		if operation == "cleanup-native-secrets" && !verified {
			return fail("Use --operator-verified to acknowledge completeness verification, operational checks, a new backup and separate key custody.")
		}
		if (installation != "" || command.Flags().Changed("installation-id")) && !contract.ValidAuditID(installation) {
			return fail("The --installation-id must be a valid installation ID.")
		}
		root, _ := command.Root().PersistentFlags().GetString("data-dir")
		layout, err := gatewaypaths.Resolve(root)
		if err != nil {
			return writeOfflineProblem(command, offlineMode(jsonOutput), installationSelectionProblem(err))
		}
		ctx, cancel := context.WithTimeout(command.Context(), 5*time.Minute)
		defer cancel()
		approval := func(ctx context.Context, owner *gatewaypaths.Ownership) error {
			snapshot, err := storage.InspectMaintenance(ctx, owner, nil)
			if err != nil {
				return err
			}
			if installation != "" && installation != snapshot.Identity.InstallationID {
				return composition.ErrCAIdentity
			}
			plan := maintenancePlan{Operation: operation, DataDir: layout.Root, InstallationID: snapshot.Identity.InstallationID, Revision: fmt.Sprint(snapshot.Identity.Revision), Actions: []string{action}, DryRun: dryRun}
			human := "Target: " + controlclient.TerminalSafePath(layout.Root) + "\nInstallation: " + snapshot.Identity.InstallationID + "\n" + action
			if dryRun {
				if err := offlineResult(command, jsonOutput, plan, human+"\nDry run: no native access or installation changes made."); err != nil {
					return err
				}
				return errDryRun
			}
			if err := offlinePlan(command, jsonOutput, plan, human); err != nil {
				return err
			}
			return confirmOffline(command, confirm, action)
		}
		result, err := composition.MaintainSecrets(ctx, layout.Root, operation, dependencies.clock, approval)
		if errors.Is(err, errDryRun) {
			return nil
		}
		if err != nil {
			remaining := "unknown"
			if result.RemainingKnown {
				remaining = fmt.Sprint(result.Remaining)
			}
			if reportErr := offlinePlan(command, jsonOutput, result, fmt.Sprintf("Completed copies: %d; remaining generations: %s; confirmed absent native items: %d; uncertain items: %d. Completeness was not established.", result.Migrated, remaining, result.Deleted, result.Uncertain)); reportErr != nil {
				return reportErr
			}
			return writeOfflineProblem(command, offlineMode(jsonOutput), maintenanceProblem(err, layout.Root))
		}
		return offlineResult(command, jsonOutput, struct {
			OK        bool   `json:"ok"`
			Operation string `json:"operation"`
			Report    any    `json:"report"`
		}{true, operation, result}, "Secret maintenance completed. Custody verification is not operational or native-platform qualification.")
	}
	command.Flags().BoolVar(&confirm, "confirm", false, "consent to the inspected plan without prompting")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "inspect without native access or installation changes")
	command.Flags().BoolVar(&jsonOutput, "json", false, "structured results and errors")
	command.Flags().StringVar(&installation, "installation-id", "", "installation identity assertion")
	if operation == "cleanup-native-secrets" {
		command.Flags().BoolVar(&verified, "operator-verified", false, "acknowledge verification, operational checks, new backup and separate key custody")
	}
	command.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return fail(offlineFlagMessage(err)) })
	return command
}
