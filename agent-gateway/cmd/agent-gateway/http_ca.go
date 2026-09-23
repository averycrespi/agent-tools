package main

import (
	"errors"
	"fmt"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/composition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/spf13/cobra"
)

func newHTTPCACmd(dependencies offlineDependencies) *cobra.Command {
	command := &cobra.Command{Use: "ca", Short: "Manage the stopped installation's interception CA"}
	configureNamespaceCommand(command)
	for _, operation := range []string{"create", "replace", "export"} {
		command.AddCommand(newHTTPCAOperation(operation, dependencies))
	}
	return command
}

func newHTTPCAOperation(operation string, dependencies offlineDependencies) *cobra.Command {
	var installation string
	var confirm bool
	usage := "agent-gateway http ca " + operation + " --installation-id ID"
	if operation != "export" {
		usage += " --confirm"
	}
	command := &cobra.Command{
		Use:   operation,
		Short: map[string]string{"create": "Create the first installation CA while stopped", "replace": "Replace the stopped installation CA and require new client trust", "export": "Export only the stopped installation's public CA certificate"}[operation],
		Long:  "Disable all service launchers first. Requires exclusive stopped installation ownership and its exact installation ID. Export writes only public PEM to stdout, without checking signing availability. Creation and replacement require client trust updates; neither enables the proxy. Never exports a private key or installs trust.",
		Args: func(command *cobra.Command, args []string) error {
			if len(args) != 0 {
				return writeOfflineProblem(command, controlclient.OutputHuman, offlineUsageProblem("CA commands do not accept positional arguments.", usage))
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			if !contract.ValidAuditID(installation) || (operation != "export" && !confirm) {
				return writeOfflineProblem(command, controlclient.OutputHuman, offlineUsageProblem("Provide the exact --installation-id and --confirm for mutations.", usage))
			}
			root, _ := command.Root().PersistentFlags().GetString("data-dir")
			certificate, err := composition.HTTPCA(command.Context(), root, installation, operation, dependencies.clock, dependencies.entropy)
			if err != nil {
				return writeOfflineProblem(command, controlclient.OutputHuman, httpCAProblem(err))
			}
			if operation == "export" {
				_, err = command.OutOrStdout().Write(certificate)
			} else {
				_, err = fmt.Fprintln(command.OutOrStdout(), "CA selected. Export the public certificate and update client trust before interception. Proxy activation is not enabled.")
			}
			return err
		},
	}
	command.Flags().StringVar(&installation, "installation-id", "", "exact existing installation ID")
	if operation != "export" {
		command.Flags().BoolVar(&confirm, "confirm", false, "confirm CA selection and required client trust updates")
	}
	command.SetFlagErrorFunc(func(command *cobra.Command, _ error) error {
		return writeOfflineProblem(command, controlclient.OutputHuman, offlineUsageProblem("A CA flag is invalid or incomplete.", usage))
	})
	return command
}

func httpCAProblem(err error) *controlclient.Problem {
	if errors.Is(err, gatewaypaths.ErrInUse) {
		return &controlclient.Problem{Code: "gateway_running", Title: "The installation is in use. Stop all Gateway launchers before CA operations.", Exit: 5}
	}
	return &controlclient.Problem{Code: "ca_unavailable", Title: "Stopped CA operation failed. Verify the installation and protected keyring. Create is first-use only; replacement is explicit after creation or restore. Authority may already be fenced or replaced; inspect before another attempt. Export alone does not verify signing availability.", Exit: 7}
}
