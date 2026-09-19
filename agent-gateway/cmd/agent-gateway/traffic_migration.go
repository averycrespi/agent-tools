package main

import (
	"fmt"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/composition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

func newTrafficMigrationCmd(dependencies offlineDependencies) *cobra.Command {
	var installation string
	var confirm bool
	var budget int64
	command := &cobra.Command{
		Use:   "migrate-traffic",
		Short: "Stage and select isolated traffic storage for a stopped installation",
		Long:  "Requires the existing installation lock and exact installation ID. Disable all service launchers first. Preserves the prior generation and interrupted stages; never performs live backfill or replay.",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return controlclient.NewInputError("Use storage migrate-traffic --installation-id ID --confirm, without positional arguments.")
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			if !confirm || installation == "" || !composition.ValidTrafficBudget(budget) {
				return controlclient.NewInputError("Provide --installation-id, --confirm, and a traffic budget between 1048576 and 17179869184 bytes.")
			}
			root, _ := command.Root().PersistentFlags().GetString("data-dir")
			generation, err := admin.NewID(dependencies.clock.Now(), dependencies.entropy)
			if err != nil {
				return err
			}
			identity, err := composition.MigrateStorage(command.Context(), root, installation, generation, budget)
			if err != nil {
				return fmt.Errorf("stopped traffic migration failed; retain original and staged generations: %w", err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "Traffic migration selected for installation %s. Prior control generation retained.\n", identity.InstallationID)
			return err
		},
	}
	command.Flags().StringVar(&installation, "installation-id", "", "exact existing installation ID")
	command.Flags().BoolVar(&confirm, "confirm", false, "confirm stopped staged migration")
	command.Flags().Int64Var(&budget, "traffic-budget-bytes", composition.DefaultTrafficBudget, "combined traffic database/WAL budget in bytes")
	return command
}
