package main

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/composition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/spf13/cobra"
)

func newHTTPCACmd(dependencies offlineDependencies) *cobra.Command {
	command := &cobra.Command{Use: "ca", Short: "Export or replace the installation's public interception CA"}
	configureNamespaceCommand(command)
	for _, operation := range []string{"replace", "export"} {
		command.AddCommand(newHTTPCAOperation(operation, dependencies))
	}
	return command
}

func newHTTPCAOperation(operation string, dependencies offlineDependencies) *cobra.Command {
	var installation, output string
	var confirm, stdout, jsonOutput bool
	usage := "agent-gateway http ca " + operation
	command := &cobra.Command{
		Use:     operation,
		Short:   map[string]string{"replace": "Create or replace the stopped installation CA", "export": "Write the public certificate without changing authority"}[operation],
		Long:    "Requires a stopped installation. Writes public PEM to <data-dir>/http-ca.pem by default; never exports private keys, installs trust or enables interception.",
		Example: "  " + usage,
	}
	fail := func(message string) error {
		return writeOfflineProblem(command, offlineMode(jsonOutput), offlineUsageProblem(message, usage))
	}
	command.Args = func(_ *cobra.Command, args []string) error {
		if len(args) != 0 {
			return fail("CA commands do not accept positional arguments.")
		}
		return nil
	}
	command.RunE = func(command *cobra.Command, _ []string) error {
		if command.Flags().Changed("installation-id") && !contract.ValidAuditID(installation) {
			return fail("The --installation-id must be a valid installation ID.")
		}
		if stdout && (command.Flags().Changed("output") || jsonOutput) {
			return fail("Choose --stdout alone, or file output with optional --json.")
		}
		if command.Flags().Changed("output") && output == "" {
			return fail("The --output path must not be empty.")
		}
		root, _ := command.Root().PersistentFlags().GetString("data-dir")
		layout, err := gatewaypaths.Resolve(root)
		if err != nil {
			return writeOfflineProblem(command, offlineMode(jsonOutput), installationSelectionProblem(err))
		}
		defaultPath := filepath.Join(layout.Root, gatewaypaths.PublicCertificateName)
		destination := output
		if destination == "" {
			destination = defaultPath
		}
		destination, err = filepath.Abs(destination)
		if err != nil {
			return fail("The --output path cannot be resolved.")
		}
		callbacks := composition.CACallbacks{}
		if operation == "replace" {
			callbacks.Validate = func(previous []byte) error { return gatewaypaths.CheckCertificateDestination(destination, previous) }
			callbacks.Confirm = func(plan composition.CAPlan) error {
				consequence := "Create the first CA; no service, proxy or trust settings change."
				if plan.HadCA {
					consequence = "Replace the existing CA; clients must update their trust before interception works."
				}
				if err := offlinePlan(command, jsonOutput, plan, fmt.Sprintf("Target: %s\nInstallation: %s\n%s", controlclient.TerminalSafePath(plan.Root), plan.InstallationID, consequence)); err != nil {
					return err
				}
				return confirmOffline(command, confirm, consequence)
			}
		}
		if !stdout {
			callbacks.Publish = func(certificate, previous []byte) error {
				if destination != defaultPath {
					previous = nil
				}
				return gatewaypaths.PublishCertificate(destination, certificate, previous)
			}
		}
		operateCA := dependencies.caOperation
		if operateCA == nil {
			operateCA = composition.HTTPCAConfirmed
		}
		certificate, err := operateCA(command.Context(), layout.Root, installation, operation, dependencies.clock, dependencies.entropy, callbacks)
		if err != nil {
			problem := httpCAProblem(err)
			nextOperation := "agent-gateway doctor"
			if errors.Is(err, composition.ErrCAPublication) {
				nextOperation = "agent-gateway http ca export --stdout"
			}
			next, renderErr := renderPathFlagCommand(nextOperation, "--data-dir", "data_dir", layout.Root)
			if renderErr == nil {
				problem.Title += " Next: " + next
			}
			return writeOfflineProblem(command, offlineMode(jsonOutput), problem)
		}
		if stdout {
			_, err = command.OutOrStdout().Write(certificate)
			return err
		}
		fingerprint, err := certificateFingerprint(certificate)
		if err != nil {
			return writeOfflineProblem(command, offlineMode(jsonOutput), httpCAProblem(err))
		}
		result := struct {
			OK          bool   `json:"ok"`
			Operation   string `json:"operation"`
			Authority   string `json:"authority_effect"`
			Path        string `json:"certificate_path"`
			Fingerprint string `json:"fingerprint"`
		}{true, "ca_" + operation, "unchanged", destination, fingerprint}
		if operation == "replace" {
			result.Authority = "changed"
		}
		return offlineResult(command, jsonOutput, result, "Public CA certificate: "+controlclient.TerminalSafePath(destination)+"\nFingerprint: "+fingerprint+"\nClient trust and proxy settings were not changed.")
	}
	command.Flags().StringVar(&installation, "installation-id", "", "optional assertion of the selected installation identity")
	command.Flags().BoolVar(&jsonOutput, "json", false, "structured results and errors")
	if operation == "export" {
		command.Flags().StringVar(&output, "output", "", "public certificate destination (default <data-dir>/http-ca.pem)")
		command.Flags().BoolVar(&stdout, "stdout", false, "stream only public PEM to stdout")
	} else {
		command.Flags().BoolVar(&confirm, "confirm", false, "consent to the displayed CA change without a prompt")
	}
	command.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return fail(offlineFlagMessage(err)) })
	return command
}

func certificateFingerprint(certificate []byte) (string, error) {
	block, rest := pem.Decode(certificate)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		return "", errors.New("invalid public certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cert.Raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func httpCAProblem(err error) *controlclient.Problem {
	effect := "unchanged"
	var mutation *composition.CAEffectError
	if errors.As(err, &mutation) {
		effect = mutation.Effect
	}
	code, title, exit := "ca_unavailable", "CA state or protected material is unavailable.", 7
	switch {
	case errors.Is(err, controlclient.ErrConfirmationRequired):
		code, title, exit = "confirmation_required", "CA change was not confirmed. Use --confirm for noninteractive consent.", 2
	case errors.Is(err, gatewaypaths.ErrInUse):
		code, title, exit = "gateway_running", "The installation is in use. Stop its launchers before CA operations.", 5
	case errors.Is(err, composition.ErrCAIdentity):
		code, title, exit = "installation_mismatch", "The --installation-id assertion does not match the selected installation.", 5
	case errors.Is(err, composition.ErrCAPublication):
		code, title = "certificate_output_unavailable", "Public certificate publication failed. Preserve unrelated output files; use http ca export --output NEW_PATH, not another replacement."
	case errors.Is(err, gatewaypaths.ErrUnsafePath):
		code, title = "unsafe_path", "The selected path has unsafe ownership, permissions or file type."
	case errors.Is(err, os.ErrNotExist):
		code, title, exit = "not_initialized", "The selected installation or required file is absent. Run init for a new installation.", 4
	case errors.Is(err, storage.ErrInspectionUnavailable):
		code, title = "inspection_unavailable", "Closed-state inspection is unavailable. Retain WAL and journal files; do not checkpoint or delete them to bypass inspection."
	case errors.Is(err, storage.ErrPlanChanged):
		code, title, exit = "plan_changed", "The inspected target changed before mutation. Inspect a fresh plan.", 5
	case errors.Is(err, storage.ErrInvalidDatabase):
		code, title = "storage_invalid", "Selected storage failed integrity or identity validation. Preserve the installation and diagnose it before changes."
	case errors.Is(err, storage.ErrStorageLatched):
		code, title = "storage_latched", "Storage requires stopped maintenance inspection before CA changes."
	}
	switch effect {
	case "changed":
		title += " CA authority changed successfully; the later step failed. Do not repeat replacement."
	case "uncertain":
		title += " CA authority outcome is uncertain. Nothing was replayed."
	default:
		title += " CA authority is unchanged."
	}
	return &controlclient.Problem{Code: code, Title: title, Exit: exit, Uncertain: effect == "uncertain"}
}
