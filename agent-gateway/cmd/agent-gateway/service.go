package main

import (
	"encoding/json"
	"fmt"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/service"
	"github.com/spf13/cobra"
)

func newServiceCmd() *cobra.Command {
	command := &cobra.Command{Use: "service", Short: "Manage the canonical logged-in user's macOS LaunchAgent", Long: "Manage only dev.agent-tools.agent-gateway in the logged-in user's gui domain. No storage initialization, credentials, binary upgrades, custom labels or migration. Installed plist selections persist; restart never changes them."}
	configureNamespaceCommand(command)
	for _, verb := range []string{"install", "start", "stop", "restart", "update", "status", "uninstall"} {
		command.AddCommand(newServiceOperation(verb))
	}
	return command
}
func newServiceOperation(verb string) *cobra.Command {
	descriptions := map[string]string{"install": "Create the private plist and logs without starting Gateway", "start": "Load the installed definition unless already loaded", "stop": "Gracefully unload and confirm process exit", "restart": "Gracefully restart with unchanged installed selections", "update": "Persist explicit settings, preserving omitted selections", "status": "Read installed settings, launchd state and separate readiness", "uninstall": "Confirm stop and remove only the canonical plist"}
	var binary, dataDir, listen, level string
	var hosts []string
	var clear bool
	var trafficBudget int64
	command := &cobra.Command{Use: verb, Short: descriptions[verb], Example: "  agent-gateway service " + verb}
	usage := "agent-gateway service " + verb
	fail := func(c *cobra.Command, message string) error {
		return writeOfflineProblem(c, controlclient.OutputHuman, offlineUsageProblem(message, usage))
	}
	command.Args = func(c *cobra.Command, args []string) error {
		if len(args) != 0 {
			return fail(c, "Service "+verb+" does not accept positional arguments.")
		}
		return nil
	}
	if verb == "install" || verb == "update" {
		command.Flags().StringVar(&binary, "binary", "", "absolute native executable path (install defaults to this executable)")
		command.Flags().StringVar(&dataDir, "data-dir", "", "absolute data directory; update preserves the installed value when omitted")
		command.Flags().StringVar(&listen, "listen", "", "exact numeric IPv4 loopback authority")
		command.Flags().Int64Var(&trafficBudget, "traffic-budget-bytes", 0, "persist combined traffic database/WAL budget; omitted updates preserve installed selection")
		command.Flags().StringVar(&level, "log-level", "", "persist serve diagnostics: warn, info, or debug")
		command.Flags().StringArrayVar(&hosts, "allowed-host", nil, "replace the complete installed hostname list (repeatable)")
		command.Flags().BoolVar(&clear, "clear-allowed-hosts", false, "clear all installed allowed hostnames")
	}
	command.SetFlagErrorFunc(func(c *cobra.Command, _ error) error {
		return fail(c, "A service "+verb+" flag is invalid or incomplete; use --help.")
	})
	command.RunE = func(c *cobra.Command, _ []string) error {
		var changes service.Changes
		settings := verb == "install" || verb == "update"
		rootData := c.Root().PersistentFlags().Changed("data-dir")
		if !settings && rootData {
			return fail(c, "This operation uses installed settings; --data-dir overrides are not accepted.")
		}
		if settings {
			if c.Flags().Changed("traffic-budget-bytes") {
				if trafficBudget < 1<<20 || trafficBudget > 16<<30 {
					return fail(c, "Traffic budget must be between 1048576 and 17179869184 bytes.")
				}
				changes.TrafficBudgetBytes = &trafficBudget
			}
			if c.Flags().Changed("binary") {
				changes.Binary = &binary
			}
			if c.Flags().Changed("listen") {
				changes.Listen = &listen
			}
			if c.Flags().Changed("log-level") {
				changes.LogLevel = &level
			}
			if c.Flags().Changed("data-dir") || rootData {
				dataDir = selectedDataDir(c, dataDir)
				changes.DataDir = &dataDir
			}
			if clear && c.Flags().Changed("allowed-host") {
				return fail(c, "Choose --allowed-host values or --clear-allowed-hosts, not both.")
			}
			if clear || c.Flags().Changed("allowed-host") {
				changes.AllowedHosts = &hosts
			}
		}
		result, err := service.Execute(c.Context(), verb, changes)
		if err != nil {
			title := fmt.Sprintf("Service %s refused: %s. Launchd: %s. %s Inspect service status before another operation.", verb, controlclient.TerminalSafePath(err.Error()), result.Launchd, controlclient.TerminalSafePath(result.Message))
			return writeOfflineProblem(c, controlclient.OutputHuman, &controlclient.Problem{Code: "service_unavailable", Title: title, Exit: 7})
		}
		data, err := json.Marshal(result)
		if err != nil {
			return commandFailure{}
		}
		_, err = fmt.Fprintln(c.OutOrStdout(), string(data))
		return err
	}
	return command
}
