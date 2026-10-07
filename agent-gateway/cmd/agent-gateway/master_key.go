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

func newRotateMasterKeyCmd(dependencies offlineDependencies) *cobra.Command {
	var confirm, dryRun, jsonOutput, retain, recoverPending bool
	var installation string
	const usage = "agent-gateway maintenance rotate-master-key --retain-recovery-keys"
	command := &cobra.Command{
		Use: "rotate-master-key", Short: "Rotate the installation master key",
		Long:    "Re-encrypt all current protected material under a fresh master key while preserving credential authority and CA identity. Requires exclusive stopped ownership and --retain-recovery-keys: both keys remain in an owner-only recovery directory outside backups. Older backups still need their original key. Safeguard retained keys separately; no key is silently destroyed. --recover reconciles an interrupted cutover without replaying encryption. Rotation cannot protect stolen copies; compromise may require upstream credential and CA replacement.",
		Example: "  " + usage + " --dry-run\n  " + usage + " --confirm\n  " + usage + " --recover --dry-run",
	}
	fail := func(message string) error {
		return writeOfflineProblem(command, offlineMode(jsonOutput), offlineUsageProblem(message, usage))
	}
	command.Args = func(_ *cobra.Command, args []string) error {
		if len(args) != 0 {
			return fail("Master-key rotation accepts no positional arguments.")
		}
		return nil
	}
	command.RunE = func(command *cobra.Command, _ []string) error {
		if !retain {
			return fail("Use --retain-recovery-keys to acknowledge separate custody of superseded keys needed by older backups.")
		}
		if (installation != "" || command.Flags().Changed("installation-id")) && !contract.ValidAuditID(installation) {
			return fail("The --installation-id must be a valid installation ID.")
		}
		root, _ := command.Root().PersistentFlags().GetString("data-dir")
		layout, err := gatewaypaths.Resolve(root)
		if err != nil {
			return writeOfflineProblem(command, offlineMode(jsonOutput), installationSelectionProblem(err))
		}
		ctx, cancel := context.WithTimeout(command.Context(), time.Minute)
		defer cancel()
		approval := func(ctx context.Context, owner *gatewaypaths.Ownership) error {
			snapshot, err := storage.InspectMaintenance(ctx, owner, nil)
			if err != nil {
				return err
			}
			if installation != "" && installation != snapshot.Identity.InstallationID {
				return composition.ErrCAIdentity
			}
			action := "Re-encrypt current secrets with a fresh key; preserve authority and CA identity; retain both recovery keys separately from unchanged backups."
			if recoverPending {
				action = "Reconcile the observed committed key identity only; retain both keys and never replay encryption. An uncommitted candidate is abandoned permanently."
			}
			plan := maintenancePlan{Operation: "rotate-master-key", DataDir: layout.Root, InstallationID: snapshot.Identity.InstallationID, Revision: fmt.Sprint(snapshot.Identity.Revision), Actions: []string{action}, DryRun: dryRun}
			human := "Target: " + controlclient.TerminalSafePath(layout.Root) + "\nInstallation: " + snapshot.Identity.InstallationID + "\n" + action
			if dryRun {
				if err := offlineResult(command, jsonOutput, plan, human+"\nDry run: no installation changes made."); err != nil {
					return err
				}
				return errDryRun
			}
			if err := offlinePlan(command, jsonOutput, plan, human); err != nil {
				return err
			}
			return confirmOffline(command, confirm, "Rotate or reconcile the installation master key and retain recovery keys?")
		}
		result, err := composition.RotateMasterKey(ctx, layout.Root, dependencies.clock, recoverPending, approval)
		if errors.Is(err, errDryRun) {
			return nil
		}
		if err != nil {
			return writeOfflineProblem(command, offlineMode(jsonOutput), maintenanceProblem(err, layout.Root))
		}
		output := struct {
			OK             bool   `json:"ok"`
			InstallationID string `json:"installation_id"`
			Disposition    string `json:"disposition"`
			RetainedKeys   string `json:"retained_keys"`
		}{true, result.Identity.InstallationID, result.Disposition, result.Retained}
		return offlineResult(command, jsonOutput, output, "Master-key operation completed: "+result.Disposition+"\nRetained recovery keys: "+result.Retained+"\nSafeguard these keys separately from backups; older backups require their original key.")
	}
	command.Flags().BoolVar(&retain, "retain-recovery-keys", false, "acknowledge separate custody of retained recovery keys")
	command.Flags().BoolVar(&recoverPending, "recover", false, "reconcile a pending cutover without replaying encryption")
	command.Flags().BoolVar(&confirm, "confirm", false, "consent to the inspected plan without prompting")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "inspect the plan without writing installation state or audit")
	command.Flags().BoolVar(&jsonOutput, "json", false, "structured results and errors")
	command.Flags().StringVar(&installation, "installation-id", "", "installation identity assertion")
	command.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return fail(offlineFlagMessage(err)) })
	return command
}
