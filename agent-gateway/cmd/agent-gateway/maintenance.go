package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/composition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/spf13/cobra"
)

var errDryRun = errors.New("read-only plan completed")

func newMaintenanceCmd(dependencies offlineDependencies) *cobra.Command {
	command := &cobra.Command{Use: "maintenance", Short: "Inspect and recover stopped installations", Long: "Choose an operation, inspect its --dry-run plan, then use --confirm for noninteractive consent. Running maintenance alone only shows help."}
	configureNamespaceCommand(command)
	command.AddCommand(newRotateMasterKeyCmd(dependencies))
	for _, operation := range []string{"reset-admin-credentials", "migrate-traffic-storage", "setup-secret-storage"} {
		command.AddCommand(newMaintenanceOperation(operation, dependencies))
	}
	return command
}

type maintenancePlan struct {
	Operation      string   `json:"operation"`
	DataDir        string   `json:"data_dir"`
	InstallationID string   `json:"installation_id"`
	Revision       string   `json:"revision"`
	Actions        []string `json:"actions"`
	SecretOutput   string   `json:"secret_output,omitempty"`
	DryRun         bool     `json:"dry_run"`
}

func newMaintenanceOperation(operation string, dependencies offlineDependencies) *cobra.Command {
	var installation, secretOutput string
	var confirm, dryRun, jsonOutput bool
	var budget int64
	descriptions := map[string]string{ //nolint:gosec // Public command descriptions, not credentials.
		"setup-secret-storage":    "Initialize encrypted secret storage",
		"reset-admin-credentials": "Reset all administrator credentials",
		"migrate-traffic-storage": "Migrate traffic to separate storage",
	}
	details := map[string]string{
		"setup-secret-storage":    "Create or verify the installation master key without reinitializing or migrating legacy credentials",
		"reset-admin-credentials": "Replace all administrator authority, retaining product state",
		"migrate-traffic-storage": "Stage isolated traffic storage, retaining prior generations without replay",
	}
	use := operation
	usage := "agent-gateway maintenance " + use
	if operation == "reset-admin-credentials" {
		usage += " --secret-output NEW_PATH"
	}
	command := &cobra.Command{Use: use, Short: descriptions[operation], Example: "  " + usage + " --dry-run", Long: details[operation] + ". Requires stopped ownership. Inspect the plan with --dry-run; execution requires default-no confirmation or --confirm. No automatic retry or fallback."}
	if operation == "reset-admin-credentials" {
		command.Long += " The replacement bearer is written once to a new 0600 file; it cannot be recovered or displayed again."
	}
	fail := func(message string) error {
		return writeOfflineProblem(command, offlineMode(jsonOutput), offlineUsageProblem(message, usage))
	}
	command.Args = func(_ *cobra.Command, args []string) error {
		if len(args) != 0 {
			return fail("This maintenance operation accepts no positional arguments.")
		}
		return nil
	}
	command.RunE = func(command *cobra.Command, args []string) error {
		if (installation != "" || command.Flags().Changed("installation-id")) && !contract.ValidAuditID(installation) {
			return fail("The --installation-id must be a valid installation ID.")
		}
		if operation == "migrate-traffic-storage" && installation == "" {
			return fail("The --installation-id flag is required for traffic migration.")
		}
		if !composition.ValidTrafficBudget(budget) {
			return fail("The --traffic-budget-bytes must be between 1MiB and 16GiB.")
		}
		needsSink := operation == "reset-admin-credentials"
		if needsSink && secretOutput == "" {
			return fail("The --secret-output flag must name a new owner-only file.")
		}
		if needsSink {
			absolute, err := filepath.Abs(secretOutput)
			if err != nil {
				return fail("The --secret-output path cannot be resolved.")
			}
			secretOutput = absolute
			if err := checkNewSecretDestination(secretOutput); err != nil {
				return fail("The --secret-output must name a nonexistent file in an existing owner-controlled directory without shared write access.")
			}
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
			if installation != "" && snapshot.Identity.InstallationID != installation {
				return composition.ErrCAIdentity
			}
			if operation != "migrate-traffic-storage" && operation != "setup-secret-storage" && snapshot.Identity.SchemaVersion != storage.CurrentSchema {
				return storage.ErrInvalidDatabase
			}
			if snapshot.Marked {
				return storage.ErrStorageLatched
			}
			if operation == "migrate-traffic-storage" && snapshot.Identity.TrafficGeneration != "" {
				return storage.ErrPlanChanged
			}
			inspectHistory := operation == "migrate-traffic-storage"
			var trafficSeal [32]byte
			var trafficErr error
			if inspectHistory {
				trafficSeal, trafficErr = composition.InspectTraffic(ctx, owner, snapshot.Identity, budget)
				if trafficErr != nil {
					return trafficErr
				}
			}
			plan := maintenancePlan{Operation: operation, DataDir: layout.Root, InstallationID: snapshot.Identity.InstallationID, Revision: fmt.Sprint(snapshot.Identity.Revision), Actions: []string{descriptions[operation]}, DryRun: dryRun}
			human := "Target: " + controlclient.TerminalSafePath(layout.Root) + "\nInstallation: " + plan.InstallationID + "\n" + strings.Join(plan.Actions, "\n")
			if needsSink {
				plan.SecretOutput = secretOutput
				human += "\nNew bearer file: " + controlclient.TerminalSafePath(secretOutput)
			}
			if dryRun {
				if err := offlineResult(command, jsonOutput, plan, human+"\nDry run: no installation changes made."); err != nil {
					return err
				}
				return errDryRun
			}
			if err := offlinePlan(command, jsonOutput, plan, human); err != nil {
				return err
			}
			if err := confirmOffline(command, confirm, descriptions[operation]+"?"); err != nil {
				return err
			}
			if err := snapshot.Revalidate(ctx, owner); err != nil {
				return err
			}
			if inspectHistory {
				currentSeal, err := composition.InspectTraffic(ctx, owner, snapshot.Identity, budget)
				if err != nil {
					return err
				}
				if currentSeal != trafficSeal {
					return storage.ErrPlanChanged
				}
			}
			if needsSink {
				return checkNewSecretDestination(secretOutput)
			}
			return nil
		}
		var identity storage.Identity
		switch operation {
		case "setup-secret-storage":
			identity, err = composition.SetupStoppedSecrets(ctx, layout.Root, dependencies.clock, approval)
		case "reset-admin-credentials":
			identity, err = executeAdminAuthority(ctx, "reset", layout.Root, admin.NewFileSecretSink(secretOutput), dependencies, approval)
		case "migrate-traffic-storage":
			var generation string
			generation, err = admin.NewID(dependencies.clock.Now(), dependencies.entropy)
			if err == nil {
				identity, err = composition.MigrateStorage(ctx, layout.Root, installation, generation, budget, approval)
			}
		}
		if errors.Is(err, errDryRun) {
			return nil
		}
		if err != nil {
			return writeOfflineProblem(command, offlineMode(jsonOutput), maintenanceProblem(err, layout.Root))
		}
		result := recoveryResult{OK: true, Operation: operation, InstallationID: identity.InstallationID, Revision: fmt.Sprint(identity.Revision)}
		human := "Maintenance completed: " + operation + "\nInstallation: " + identity.InstallationID
		if needsSink {
			next, err := renderBearerCommand("agent-gateway doctor --online", secretOutput)
			if err == nil {
				next, err = renderPathFlagCommand(next, "--data-dir", "data_dir", layout.Root)
			}
			if err != nil {
				return err
			}
			human += "\nNew administrator bearer file: " + controlclient.TerminalSafePath(secretOutput) + "\nThe bearer cannot be shown again. After starting Gateway, use: " + next
		}
		return offlineResult(command, jsonOutput, result, human)
	}
	command.Flags().StringVar(&installation, "installation-id", "", "installation identity assertion (required for traffic migration)")
	command.Flags().BoolVar(&confirm, "confirm", false, "consent to the inspected plan without prompting")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "inspect the plan without writing installation state or audit")
	command.Flags().BoolVar(&jsonOutput, "json", false, "structured results and errors")
	if operation == "reset-admin-credentials" {
		command.Flags().StringVar(&secretOutput, "secret-output", "", "new owner-only file for the replacement administrator bearer")
	}
	budget = composition.DefaultTrafficBudget
	if operation != "setup-secret-storage" {
		storageSizeFlag(command.Flags(), &budget, composition.DefaultTrafficBudget, "selected traffic database/WAL budget")
	}
	command.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return fail(offlineFlagMessage(err)) })
	return command
}

func checkNewSecretDestination(path string) error {
	if err := gatewaypaths.ValidateOutputDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return admin.ErrSecretPublication
	}
	return nil
}

func maintenanceProblem(err error, root string) (problem *controlclient.Problem) {
	defer func() {
		if problem != nil {
			problem.Local = diagnostics.Snapshot("maintenance", "stopped operation", root, err)
		}
	}()
	code, title, exit := "maintenance_unavailable", "Maintenance did not complete. Preserve all retained generations and output files; no operation was replayed.", 7
	switch {
	case errors.Is(err, controlclient.ErrConfirmationRequired):
		code, title, exit = "confirmation_required", "No maintenance changes made. Use --confirm for noninteractive consent.", 2
	case errors.Is(err, gatewaypaths.ErrKeyRotationPending):
		code, title, exit = "master_key_rotation_pending", "Master-key rotation requires explicit stopped reconciliation. Preserve all key and database files; inspect maintenance rotate-master-key --retain-recovery-keys --recover --dry-run. No encryption was replayed.", 7
	case errors.Is(err, gatewaypaths.ErrInUse):
		code, title, exit = "gateway_running", "No maintenance changes made. Stop the selected installation and its launchers first.", 5
	case errors.Is(err, storage.ErrPlanChanged), errors.Is(err, composition.ErrCAIdentity):
		code, title, exit = "plan_changed", "No maintenance changes made. The target or confirmed plan does not match; inspect a new --dry-run.", 5
	case errors.Is(err, gatewaypaths.ErrUnsafePath):
		code, title, exit = "unsafe_path", "Selected paths failed ownership, permissions or file-type checks. Preserve them; do not delete artifacts to bypass validation.", 5
	case errors.Is(err, storage.ErrInvalidDatabase):
		code, title = "storage_invalid", "Selected storage failed integrity, schema or identity validation. Preserve the installation and obtain a qualified recovery plan."
	case errors.Is(err, storage.ErrTrafficUnselected):
		code, title = "traffic_unselected", "History is disabled until maintenance migrate-traffic-storage selects a generation using the exact --installation-id; security-ready serving does not require history."
	case errors.Is(err, admin.ErrNotInitialized):
		code, title, exit = "not_initialized", "No administrator authority exists. Init can complete demonstrably missing setup without resetting authority.", 4
	case errors.Is(err, admin.ErrAlreadyInitialized):
		code, title, exit = "already_initialized", "Administrator authority already exists and was preserved. Select its known bearer file; do not reset it because output is unavailable.", 5
	case errors.Is(err, storage.ErrInspectionUnavailable):
		code, title = "inspection_unavailable", "Read-only inspection is blocked by uncheckpointed WAL or journal state. No maintenance changes made; retain all files. A qualified WAL-aware recovery plan is required."
	case errors.Is(err, storage.ErrStorageLatched):
		code, title = "storage_latched", "Recovery state is unknown or incompatible with this operation. No fallback reset, restore or deletion was attempted."
	case errors.Is(err, composition.ErrSecretCustody), errors.Is(err, keyring.ErrCustodyUnavailable), errors.Is(err, keyring.ErrIncompleteGeneration):
		code, title, exit = "secret_storage_unavailable", "The master-key is missing, unsafe, incomplete or mismatched. Preserve the key and database and obtain a stopped recovery plan; setup never replaces an established key.", 7
	case errors.Is(err, storage.ErrLegacyCustody):
		code, title, exit = "legacy_secret_custody", "Legacy secret dependencies or cleanup provenance remain unresolved. Preserve the installation and complete the pre-removal upgrade procedure with the prior supported version; this version cannot migrate or clean native secrets.", 7
	case errors.Is(err, admin.ErrSecretPublication):
		code, title, exit = "secret_output_unavailable", "The --secret-output file could not be safely published. Authority was not activated from this output.", 2
	case errors.Is(err, os.ErrNotExist):
		code, title, exit = "not_initialized", "The selected installation or required file is absent. Only init creates a new installation.", 4
	}
	if detail := pathValidationDetail(err); detail != "" {
		title = "Cannot inspect " + detail
	}
	var effect *storage.OperationError
	uncertain := false
	if errors.As(err, &effect) {
		switch effect.Effect {
		case "staged":
			title += " Staging or replacement output may exist; current installation authority was not selected from it."
		case "changed":
			title += " The replacement was selected; a later step failed. Do not repeat the operation."
		case "uncertain":
			title += " Mutation or selection outcome is uncertain; do not replay."
			uncertain = true
		}
	}
	next, renderErr := renderInstallationCommand("agent-gateway doctor", root)
	if renderErr == nil {
		title += " Next: " + next
	}
	return &controlclient.Problem{Code: code, Title: title, Exit: exit, Uncertain: uncertain}
}
