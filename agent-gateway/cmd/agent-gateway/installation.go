package main

import (
	"encoding/json"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/installation"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/spf13/cobra"
)

func installationSelectionProblem(err error) *controlclient.OnlineError {
	if errors.Is(err, paths.ErrMigrationRequired) {
		return controlclient.NewInputError("Legacy installation selection is ambiguous. Select the existing --data-dir explicitly or follow docs/operators/installation-migration.md; do not initialize another root.")
	}
	return controlclient.NewInputError("The selected data directory is invalid.")
}

func newInstallationCmd() *cobra.Command {
	namespace := &cobra.Command{Use: "installation", Short: "Inspect and explicitly migrate stopped installation paths"}
	configureNamespaceCommand(namespace)
	var selection installation.Selection
	command := &cobra.Command{
		Use:   "migrate",
		Short: "Preflight a stopped macOS installation; --confirm performs one atomic path exchange",
		Long:  "Move one complete stopped installation between sibling roots without recovery, reinitialization or credential changes. Both launchd jobs must be absent and both plists archived outside LaunchAgents. Run serially; see docs/operators/installation-migration.md. Without --confirm this only inspects paths, identity, service/process absence and existing locks.",
		Args: func(command *cobra.Command, args []string) error {
			if len(args) != 0 {
				return writeOfflineProblem(command, controlclient.OutputJSON, namespaceUsageProblem(command, "Installation migration requires named selections, not positional arguments."))
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			result, err := installation.Migrate(command.Context(), selection)
			if err != nil {
				return writeOfflineProblem(command, controlclient.OutputJSON, controlclient.NewInputError("Migration refused: "+err.Error()))
			}
			if err = json.NewEncoder(command.OutOrStdout()).Encode(result); err != nil {
				return commandFailure{}
			}
			return nil
		},
	}
	command.Flags().StringVar(&selection.Source, "source", "", "explicit absolute canonical source root")
	command.Flags().StringVar(&selection.Destination, "destination", "", "explicit absolute sibling destination root")
	command.Flags().StringVar(&selection.InstallationID, "installation-id", "", "expected existing installation ID (not a credential)")
	command.Flags().StringVar(&selection.ServiceBinary, "service-binary", "", "absolute executable from the archived service argv")
	command.Flags().BoolVar(&selection.Confirm, "confirm", false, "confirm this exact source/destination/installation selection after read-only preflight")
	namespace.AddCommand(command)
	return namespace
}
