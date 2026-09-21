package main

import (
	"errors"
	"fmt"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/composition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/spf13/cobra"
)

func newTrafficMigrationCmd(dependencies offlineDependencies) *cobra.Command {
	var installation string
	var confirm bool
	var budget int64
	const usage = "agent-gateway storage migrate-traffic --installation-id ID --confirm"
	command := &cobra.Command{
		Use:   "migrate-traffic",
		Short: "Stage and select isolated traffic storage for a stopped installation",
		Long:  "Requires the existing installation lock and exact installation ID. Disable all service launchers first. Preserves the prior generation and interrupted stages; never performs live backfill or replay.",
		Args: func(command *cobra.Command, args []string) error {
			if len(args) != 0 {
				return writeOfflineProblem(command, controlclient.OutputHuman, offlineUsageProblem("Traffic migration does not accept positional arguments.", usage))
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			if !confirm || installation == "" || !composition.ValidTrafficBudget(budget) {
				return writeOfflineProblem(command, controlclient.OutputHuman, offlineUsageProblem("Provide --installation-id, --confirm, and a traffic budget between 1048576 and 17179869184 bytes.", usage))
			}
			root, _ := command.Root().PersistentFlags().GetString("data-dir")
			generation, err := admin.NewID(dependencies.clock.Now(), dependencies.entropy)
			if err != nil {
				return writeOfflineProblem(command, controlclient.OutputHuman, trafficMigrationProblem(err))
			}
			identity, err := composition.MigrateStorage(command.Context(), root, installation, generation, budget)
			if err != nil {
				return writeOfflineProblem(command, controlclient.OutputHuman, trafficMigrationProblem(err))
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "Traffic migration selected for installation %s. Prior control generation retained.\n", identity.InstallationID)
			return err
		},
	}
	command.Flags().StringVar(&installation, "installation-id", "", "exact existing installation ID")
	command.Flags().BoolVar(&confirm, "confirm", false, "confirm stopped staged migration")
	command.Flags().Int64Var(&budget, "traffic-budget-bytes", composition.DefaultTrafficBudget, "combined traffic database/WAL budget in bytes")
	command.SetFlagErrorFunc(func(command *cobra.Command, _ error) error {
		return writeOfflineProblem(command, controlclient.OutputHuman, offlineUsageProblem("A traffic migration flag is invalid or incomplete.", usage))
	})
	return command
}

func trafficMigrationProblem(err error) *controlclient.Problem {
	if errors.Is(err, gatewaypaths.ErrInUse) {
		return &controlclient.Problem{Code: "gateway_running", Title: "The installation is in use. Stop all Gateway launchers before traffic migration.", Exit: 5}
	}
	return &controlclient.Problem{Code: "storage_unavailable", Title: "Stopped traffic migration failed. Retain the original and staged generations; inspect the installation before another attempt. The new pair may already be selected.", Exit: 7}
}
