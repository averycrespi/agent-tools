package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/composition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
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

// Doctor's opt-in closed-storage inspection remains separate from ordinary init.
func inspectInit(ctx context.Context, owner *gatewaypaths.Ownership) (initInspection, error) {
	var result initInspection
	snapshot, err := storage.InspectClosedInstallation(ctx, owner.Layout(), func(tx *sql.Tx) error {
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
		result.Traffic, err = composition.InspectTraffic(ctx, owner, snapshot.Identity, composition.DefaultTrafficBudget)
	} else if err == nil && result.Admin {
		err = storage.ErrTrafficUnselected
	}
	return result, err
}

func newInitCmd(dependencies offlineDependencies) *cobra.Command {
	var confirm, jsonOutput, resume bool
	var secretOutput string
	command := &cobra.Command{Use: "init", Aliases: []string{"initialize"}, Short: "Create a local installation", Long: "Create a fully provisioned installation only in an absent or empty safe directory. Use --resume explicitly for recognized interrupted initialization; unknown or damaged state is never repaired. Requires confirmation or --confirm. Does not start listeners, install client trust, change shell settings or install services. Initial bearer publication uses a new 0600 file; it cannot be recovered or displayed again. Secrets are never displayed.", Example: "  agent-gateway init --confirm\n  agent-gateway init --resume --confirm"}
	fail := func(message string) error {
		return writeOfflineProblem(command, offlineMode(jsonOutput), offlineUsageProblem(message, "agent-gateway init [--resume] --confirm"))
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
		serve, err := renderServeCommand(layout.Root, root == "")
		if err != nil {
			return fail("The selected path is too long to render safely.")
		}
		doctor, _ := renderInstallationCommand("agent-gateway doctor", layout.Root)
		resumeCommand, _ := renderInstallationCommand("agent-gateway init --resume", layout.Root)
		refused := func(message string) error {
			return writeOfflineProblem(command, offlineMode(jsonOutput), &controlclient.Problem{Code: "initialization_blocked", Title: message + " Preserve all artifacts; inspect with: " + doctor, Exit: 5})
		}
		fresh := false
		var plan gatewaypaths.InitializationPlan
		rootErr := gatewaypaths.InspectRoot(layout.Root)
		switch {
		case errors.Is(rootErr, os.ErrNotExist):
			fresh = true
		case rootErr != nil:
			return writeOfflineProblem(command, offlineMode(jsonOutput), maintenanceProblem(rootErr, layout.Root))
		default:
			directory, err := os.Open(layout.Root)
			if err != nil {
				return refused("The selected directory cannot be inspected.")
			}
			entries, readErr := directory.Readdirnames(1)
			if errors.Is(readErr, io.EOF) {
				readErr = nil
			}
			if err := errors.Join(readErr, directory.Close()); err != nil {
				return refused("The selected directory cannot be inspected.")
			}
			fresh = len(entries) == 0
			if !fresh {
				var complete bool
				plan, complete, err = gatewaypaths.ReadInitialization(layout)
				_, evidenceErr := os.Lstat(filepath.Join(layout.Root, gatewaypaths.InitializationName))
				if errors.Is(err, os.ErrNotExist) && errors.Is(evidenceErr, os.ErrNotExist) {
					// Pre-protocol installations are never adopted or repaired by init. Only
					// safe control/lock presence is recognized; doctor owns integrity checks.
					if gatewaypaths.ValidateOwnerOnlyFile(layout.Database) != nil || gatewaypaths.ValidateOwnerOnlyFile(layout.Lock) != nil {
						return refused("The selected directory is nonempty and has no recognized initialization evidence.")
					}
					return offlineResult(command, jsonOutput, map[string]any{"ok": true, "operation": "init", "data_dir": layout.Root, "changed": false, "state": "existing_installation", "next_command": doctor}, "Existing installation files detected. Nothing changed; init is creation-only.\nDiagnose: "+doctor+"\nStart an established installation: "+serve)
				} else if err != nil {
					return refused("Initialization evidence is unsafe, inconsistent or damaged.")
				}
				if complete {
					return offlineResult(command, jsonOutput, map[string]any{"ok": true, "operation": "init", "data_dir": layout.Root, "changed": false, "state": "already_initialized", "next_command": serve}, "Gateway is already initialized; setup is complete. Nothing changed.\nStart: "+serve+"\nDiagnose: "+doctor)
				}
				if !resume {
					return refused("Initialization was interrupted. Continue explicitly with: " + resumeCommand)
				}
			}
		}
		if resume && fresh {
			return refused("There is no recognized interrupted initialization to resume.")
		}
		bearerPath := secretOutput
		if bearerPath == "" {
			if fresh {
				bearerPath = layout.AdminBearer
			} else {
				bearerPath = plan.Bearer
			}
		}
		bearerPath, err = filepath.Abs(bearerPath)
		if err != nil || len(bearerPath) > 4096 {
			return fail("The selected bearer path cannot be resolved safely.")
		}
		if !fresh && bearerPath != plan.Bearer {
			return refused("The bearer destination differs from the recorded initialization consent.")
		}
		bearerInRoot := filepath.Dir(bearerPath) == layout.Root
		realRoot, rootResolutionErr := filepath.EvalSymlinks(layout.Root)
		realParent, parentResolutionErr := filepath.EvalSymlinks(filepath.Dir(bearerPath))
		if rootResolutionErr == nil && parentResolutionErr == nil && realRoot == realParent {
			bearerInRoot = true
		}
		if fresh && bearerInRoot && filepath.Base(bearerPath) != gatewaypaths.AdminBearerName {
			reserved := map[string]bool{gatewaypaths.DatabaseName: true, gatewaypaths.LockName: true, gatewaypaths.MasterKeyName: true, gatewaypaths.PublicCertificateName: true, gatewaypaths.InitializationName: true, gatewaypaths.RunMarkerName: true, gatewaypaths.MutationMarkerName: true, gatewaypaths.BackupsName: true}
			if reserved[filepath.Base(bearerPath)] || strings.HasPrefix(filepath.Base(bearerPath), "traffic-") {
				return fail("The bearer destination conflicts with a reserved installation path. Choose a new credential file.")
			}
		}
		if fresh && bearerPath != layout.AdminBearer {
			if err := checkNewSecretDestination(bearerPath); err != nil {
				return fail("Initial bearer output requires a new file in an existing owner-controlled directory.")
			}
		}
		certificatePath := filepath.Join(layout.Root, gatewaypaths.PublicCertificateName)
		description := "Create Gateway storage, encrypted custody, administrator access and MCP/HTTP/Git proxy capability"
		if !fresh {
			description = "Continue only proven-missing setup in the recorded installation"
		}
		if err := offlinePlan(command, jsonOutput, map[string]any{"operation": "init", "data_dir": layout.Root, "admin_bearer_file": bearerPath, "certificate_path": certificatePath, "resume": resume}, fmt.Sprintf("Target: %s\nProposed changes: %s\nAdministrator bearer file: %s\nPublic certificate: %s", controlclient.TerminalSafePath(layout.Root), description, controlclient.TerminalSafePath(bearerPath), controlclient.TerminalSafePath(certificatePath))); err != nil {
			return err
		}
		if err := confirmOffline(command, confirm, "Prepare this installation without starting services or installing client trust?"); err != nil {
			return writeOfflineProblem(command, offlineMode(jsonOutput), maintenanceProblem(err, layout.Root))
		}
		if fresh {
			installation, err := admin.NewID(dependencies.clock.Now(), dependencies.entropy)
			if err != nil {
				return refused("Could not prepare an installation identity. No installation was created.")
			}
			traffic, err := admin.NewID(dependencies.clock.Now(), dependencies.entropy)
			if err != nil {
				return refused("Could not prepare storage identity. No installation was created.")
			}
			plan = gatewaypaths.InitializationPlan{Version: 1, Root: layout.Root, Bearer: bearerPath, Installation: installation, Traffic: traffic}
		}
		var owner *gatewaypaths.Ownership
		if fresh {
			owner, err = gatewaypaths.AcquireInitialization(layout.Root)
		} else {
			owner, err = gatewaypaths.AcquireStoppedExisting(layout.Root)
		}
		if err != nil {
			return writeOfflineProblem(command, offlineMode(jsonOutput), maintenanceProblem(err, layout.Root))
		}
		defer func() { _ = owner.Close() }()
		if fresh {
			if err := gatewaypaths.BeginInitialization(owner, plan); err != nil {
				return refused("The selected directory changed before creation; no existing files were replaced.")
			}
		}
		identity, err := composition.InitializeOwned(command.Context(), owner, plan, dependencies.clock, dependencies.entropy)
		err = errors.Join(err, owner.Close())
		fingerprint := ""
		if err == nil {
			fingerprint, err = certificateFingerprint(identity.Certificate)
		}
		if err != nil {
			problem := &controlclient.Problem{Code: "initialization_incomplete", Title: "Initialization could not complete safely. Preserve all artifacts. Inspect with " + doctor + "; recognized missing work can continue with " + resumeCommand, Exit: 5}
			problem.Local = diagnostics.Snapshot("init", "complete installation", layout.Root, err)
			return writeOfflineProblem(command, offlineMode(jsonOutput), problem)
		}
		result := map[string]any{"ok": true, "operation": "init", "data_dir": layout.Root, "admin_bearer_file": bearerPath, "certificate_path": certificatePath, "fingerprint": fingerprint, "installation_id": identity.InstallationID, "revision": fmt.Sprint(identity.Revision), "next_command": serve, "changed": true}
		return offlineResult(command, jsonOutput, result, "Gateway setup is complete.\nData directory: "+controlclient.TerminalSafePath(layout.Root)+"\nAdministrator bearer file: "+controlclient.TerminalSafePath(bearerPath)+"\nThe bearer cannot be shown again; retain its file securely.\nPublic certificate: "+controlclient.TerminalSafePath(certificatePath)+"\nFingerprint: "+fingerprint+"\nStart: "+serve+"\nBrowser: http://127.0.0.1:8210 — sign in with the administrator bearer file\nClients must explicitly trust the public CA certificate for HTTPS interception. No client trust or shell settings were installed.")
	}
	command.Flags().BoolVar(&confirm, "confirm", false, "consent to the displayed setup without a prompt")
	command.Flags().BoolVar(&resume, "resume", false, "continue recognized interrupted initialization without replacing existing material")
	command.Flags().BoolVar(&jsonOutput, "json", false, "structured results and errors")
	command.Flags().StringVar(&secretOutput, "secret-output", "", "initial bearer destination (default <data-dir>/admin-bearer; resume retains the recorded path)")
	command.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return fail(offlineFlagMessage(err)) })
	return command
}
