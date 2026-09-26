package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/composition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/spf13/cobra"
)

type initInspection struct {
	Snapshot storage.Inspection
	Admin    bool
	CA       httpca.Inspection
	Traffic  [32]byte
}

func inspectInit(ctx context.Context, owner *gatewaypaths.Ownership) (initInspection, error) {
	return inspectInitLayout(ctx, owner.Layout())
}

func inspectInitLayout(ctx context.Context, layout gatewaypaths.Layout) (initInspection, error) {
	var result initInspection
	snapshot, err := storage.InspectClosedInstallation(ctx, layout, func(tx *sql.Tx) error {
		var err error
		result.Admin, err = admin.InitializedTx(ctx, tx)
		if err != nil {
			return err
		}
		result.CA, err = httpca.InspectTx(ctx, tx)
		return err
	})
	result.Snapshot = snapshot
	if err == nil && snapshot.Marked {
		err = storage.ErrStorageLatched
	}
	if err == nil && snapshot.Identity.TrafficGeneration != "" {
		result.Traffic, err = composition.InspectTrafficLayout(ctx, layout, snapshot.Identity, composition.DefaultTrafficBudget)
	} else if err == nil && result.Admin {
		err = storage.ErrTrafficUnselected
	}
	return result, err
}

func (expected initInspection) revalidate(ctx context.Context, owner *gatewaypaths.Ownership) error {
	if err := expected.Snapshot.Revalidate(ctx, owner); err != nil {
		return err
	}
	if expected.Snapshot.Identity.TrafficGeneration != "" {
		current, err := composition.InspectTraffic(ctx, owner, expected.Snapshot.Identity, composition.DefaultTrafficBudget)
		if err != nil {
			return err
		}
		if current != expected.Traffic {
			return storage.ErrPlanChanged
		}
	}
	return nil
}

func newInitCmd(dependencies offlineDependencies) *cobra.Command {
	var confirm, jsonOutput bool
	var secretOutput string
	command := &cobra.Command{Use: "init", Aliases: []string{"initialize"}, Short: "Initialize or complete local setup", Long: "Preserve existing credentials, configuration and CA. Inspect proposed changes before consent; --confirm permits noninteractive setup. Does not recover storage, reset authority, change services, enable interception or install trust. Initial bearer publication uses a new 0600 file; it cannot be recovered or displayed again.", Example: "  agent-gateway init --confirm"}
	fail := func(message string) error {
		return writeOfflineProblem(command, offlineMode(jsonOutput), offlineUsageProblem(message, "agent-gateway init --confirm"))
	}
	command.Args = func(_ *cobra.Command, args []string) error {
		if len(args) != 0 {
			return fail("Init accepts no positional arguments.")
		}
		return nil
	}
	command.RunE = func(command *cobra.Command, _ []string) error {
		root, _ := command.Root().PersistentFlags().GetString("data-dir")
		layout, err := gatewaypaths.Resolve(root)
		if err != nil {
			return writeOfflineProblem(command, offlineMode(jsonOutput), installationSelectionProblem(err))
		}
		bearerPath := secretOutput
		if bearerPath == "" {
			bearerPath = layout.AdminBearer
		}
		bearerPath, err = filepath.Abs(bearerPath)
		if err != nil {
			return fail("The selected bearer path cannot be resolved.")
		}
		certificatePath := filepath.Join(layout.Root, gatewaypaths.PublicCertificateName)
		fresh := false
		running := false
		var inspection initInspection
		owner, err := gatewaypaths.AcquireStoppedExisting(layout.Root)
		switch {
		case err == nil:
			inspection, err = inspectInit(command.Context(), owner)
			err = errors.Join(err, owner.Close())
		case errors.Is(err, gatewaypaths.ErrInUse):
			running = true
			inspection, err = inspectInitLayout(command.Context(), layout)
		case errors.Is(err, os.ErrNotExist):
			// Only a missing or genuinely empty directory establishes first creation.
			entries, readErr := os.ReadDir(layout.Root)
			if errors.Is(readErr, os.ErrNotExist) {
				fresh = true
				err = nil
			} else if readErr == nil && len(entries) == 0 && gatewaypaths.InspectRoot(layout.Root) == nil {
				fresh = true
				err = nil
			}
		}
		if err != nil {
			problem := maintenanceProblem(err, layout.Root)
			if running && errors.Is(err, storage.ErrInspectionUnavailable) {
				next, _ := renderInstallationCommand("agent-gateway init", layout.Root)
				problem = &controlclient.Problem{Code: "setup_inspection_blocked", Title: "Cannot verify setup while Gateway has active WAL or journal data. Nothing changed. Stop the selected Gateway, then run: " + next, Exit: 5}
			} else {
				problem.Title += " No setup changes made."
			}
			return writeOfflineProblem(command, offlineMode(jsonOutput), problem)
		}
		if !fresh && inspection.Admin {
			bearer, bearerErr := controlclient.AcquireAdminBearer(controlclient.BearerOptions{FilePath: bearerPath})
			if bearerErr == nil {
				_, bearerErr = storage.InspectClosedInstallation(command.Context(), layout, func(tx *sql.Tx) error {
					_, err := admin.AuthenticateTx(command.Context(), tx, bearer, dependencies.clock.Now())
					return err
				})
			}
			if bearerErr != nil {
				return fail("Existing administrator authority is retained, but its selected bearer file is unavailable. Select a known credential with --secret-output; missing material never authorizes a reset.")
			}
		}
		if !fresh && inspection.CA.Revision != "0" && !inspection.CA.Selected {
			return fail("Existing CA authority is invalidated or unavailable. Init will not replace it; use http ca replace after diagnosis.")
		}
		if !fresh && inspection.CA.Revision == "0" && inspection.CA.Unsettled {
			return fail("CA setup has unresolved protected-generation evidence. Init will not replay it; use doctor and explicit CA replacement after diagnosis.")
		}
		createCA := fresh || inspection.CA.Revision == "0"
		publish := true
		if !fresh {
			if err := gatewaypaths.CheckCertificateDestination(certificatePath, inspection.CA.Certificate); err != nil {
				return fail("The managed http-ca.pem file is unsafe or does not match current CA metadata; preserve it and export to a new path.")
			}
			_, statErr := os.Lstat(certificatePath)
			if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				return fail("The managed http-ca.pem file cannot be inspected.")
			}
			publish = errors.Is(statErr, os.ErrNotExist)
		}
		if (fresh || !inspection.Admin) && (!fresh || bearerPath != layout.AdminBearer) {
			if err := checkNewSecretDestination(bearerPath); err != nil {
				return fail("Initial bearer output requires a new file in an existing owner-controlled directory.")
			}
		}
		actions := []string{}
		if fresh {
			actions = append(actions, "Create storage and initial administrator authority")
		} else if !inspection.Admin {
			actions = append(actions, "Complete initial administrator authority; no historical credential exists")
		}
		if createCA {
			actions = append(actions, "Create initial CA in protected storage")
		}
		if publish {
			actions = append(actions, "Publish public certificate at "+controlclient.TerminalSafePath(certificatePath))
		}
		if len(actions) != 0 && running {
			return writeOfflineProblem(command, offlineMode(jsonOutput), maintenanceProblem(gatewaypaths.ErrInUse, layout.Root))
		}
		if len(actions) != 0 {
			plan := map[string]any{"operation": "init", "data_dir": layout.Root, "actions": actions}
			if err := offlinePlan(command, jsonOutput, plan, fmt.Sprintf("Target: %s\nProposed changes: %v", controlclient.TerminalSafePath(layout.Root), actions)); err != nil {
				return err
			}
			if err := confirmOffline(command, confirm, "Prepare the displayed missing setup without changing services or client trust?"); err != nil {
				return writeOfflineProblem(command, offlineMode(jsonOutput), maintenanceProblem(err, layout.Root))
			}
		}
		if fresh || !inspection.Admin {
			approval := func(ctx context.Context, owner *gatewaypaths.Ownership) error {
				if fresh {
					if _, err := os.Lstat(owner.Layout().Database); !errors.Is(err, os.ErrNotExist) {
						return storage.ErrPlanChanged
					}
					if _, err := os.Lstat(bearerPath); !errors.Is(err, os.ErrNotExist) {
						return admin.ErrSecretPublication
					}
					return nil
				}
				return inspection.revalidate(ctx, owner)
			}
			if _, err = executeAdminAuthority(command.Context(), "initialize", layout.Root, admin.NewFileSecretSink(bearerPath), dependencies, approval); err != nil {
				return writeOfflineProblem(command, offlineMode(jsonOutput), maintenanceProblem(err, layout.Root))
			}
		} else if len(actions) != 0 {
			owner, err := gatewaypaths.AcquireStoppedExisting(layout.Root)
			if err != nil {
				return writeOfflineProblem(command, offlineMode(jsonOutput), maintenanceProblem(err, layout.Root))
			}
			err = inspection.revalidate(command.Context(), owner)
			err = errors.Join(err, owner.Close())
			if err != nil {
				return writeOfflineProblem(command, offlineMode(jsonOutput), maintenanceProblem(err, layout.Root))
			}
		}
		if createCA || publish {
			operation := "export"
			if createCA {
				operation = "create"
			}
			operateCA := dependencies.caOperation
			if operateCA == nil {
				operateCA = composition.HTTPCAConfirmed
			}
			_, err = operateCA(command.Context(), layout.Root, "", operation, dependencies.clock, dependencies.entropy, composition.CACallbacks{Validate: func(previous []byte) error {
				return gatewaypaths.CheckCertificateDestination(certificatePath, previous)
			}, Publish: func(cert, _ []byte) error { return gatewaypaths.PublishCertificate(certificatePath, cert, nil) }})
			if err != nil {
				problem := httpCAProblem(err)
				problem.Title = "Storage and administrator authority are retained. " + problem.Title + " Diagnose with doctor; init only completes demonstrably missing setup."
				return writeOfflineProblem(command, offlineMode(jsonOutput), problem)
			}
		}
		finalInspection, err := inspectInitLayout(command.Context(), layout)
		if err != nil {
			return writeOfflineProblem(command, offlineMode(jsonOutput), maintenanceProblem(err, layout.Root))
		}
		fingerprint, err := certificateFingerprint(finalInspection.CA.Certificate)
		if err != nil {
			return writeOfflineProblem(command, offlineMode(jsonOutput), httpCAProblem(err))
		}
		serve, err := renderServeCommand(layout.Root, root == "")
		if err != nil {
			return fail("The selected path is too long to render safely.")
		}
		result := map[string]any{"ok": true, "operation": "init", "data_dir": layout.Root, "admin_bearer_file": bearerPath, "certificate_path": certificatePath, "fingerprint": fingerprint, "installation_id": finalInspection.Snapshot.Identity.InstallationID, "revision": fmt.Sprint(finalInspection.Snapshot.Identity.Revision), "next_command": serve, "changed": len(actions) != 0}
		return offlineResult(command, jsonOutput, result, "Gateway setup is complete. Existing authority is preserved.\nAdministrator bearer file: "+controlclient.TerminalSafePath(bearerPath)+"\nBearer publication is one-time; it cannot be shown again.\nPublic certificate: "+controlclient.TerminalSafePath(certificatePath)+"\nFingerprint: "+fingerprint+"\nStart: "+serve)
	}
	command.Flags().BoolVar(&confirm, "confirm", false, "consent to creating demonstrably missing setup without a prompt")
	command.Flags().BoolVar(&jsonOutput, "json", false, "structured results and errors")
	command.Flags().StringVar(&secretOutput, "secret-output", "", "initial bearer destination, or known existing bearer file (default <data-dir>/admin-bearer)")
	command.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return fail(offlineFlagMessage(err)) })
	return command
}
