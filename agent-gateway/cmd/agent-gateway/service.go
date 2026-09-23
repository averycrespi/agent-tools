package main

import (
	"encoding/json"
	"fmt"
	"strings"

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
	var binary, dataDir, listen, proxyListen, level, output string
	var jsonOutput bool
	var hosts []string
	var clear, clearProxy bool
	var trafficBudget int64
	command := &cobra.Command{Use: verb, Short: descriptions[verb], Example: "  agent-gateway service " + verb}
	usage := "agent-gateway service " + verb
	fail := func(c *cobra.Command, message string) error {
		return writeOfflineProblem(c, selectedOutputMode(c, output, jsonOutput), offlineUsageProblem(message, usage))
	}
	command.Flags().StringVar(&output, "output", "human", "output mode: human or json (does not change serve settings)")
	command.Flags().BoolVar(&jsonOutput, "json", false, "shorthand for --output json")
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
		command.Flags().StringVar(&proxyListen, "http-proxy-listen", "", "enable proxy on a separate numeric IPv4 loopback authority; requires an existing CA")
		command.Flags().BoolVar(&clearProxy, "clear-http-proxy-listen", false, "disable the installed HTTP proxy listener")
		command.Flags().Int64Var(&trafficBudget, "traffic-budget-bytes", 0, "persist combined traffic database/WAL budget; omitted updates preserve installed selection")
		command.Flags().StringVar(&level, "log-level", "", "persist serve diagnostics: warn, info, or debug")
		command.Flags().StringArrayVar(&hosts, "allowed-host", nil, "replace the complete installed hostname list (repeatable)")
		command.Flags().BoolVar(&clear, "clear-allowed-hosts", false, "clear all installed allowed hostnames")
	}
	command.SetFlagErrorFunc(func(c *cobra.Command, _ error) error {
		return fail(c, "A service "+verb+" flag is invalid or incomplete; use --help.")
	})
	command.RunE = func(c *cobra.Command, _ []string) error {
		options, err := resolveExecutionOptions(executionOptionInput{Output: output, OutputSet: c.Flags().Changed("output"), JSON: jsonOutput})
		if err != nil {
			return fail(c, "Choose either --output human or --output json; --json is the JSON shorthand.")
		}
		var changes service.Changes
		settings := verb == "install" || verb == "update"
		rootData := c.Root().PersistentFlags().Changed("data-dir")
		if !settings && rootData {
			return fail(c, "This operation uses installed settings; --data-dir overrides are not accepted.")
		}
		if settings {
			if clearProxy && c.Flags().Changed("http-proxy-listen") {
				return fail(c, "Choose --http-proxy-listen or --clear-http-proxy-listen, not both.")
			}
			if c.Flags().Changed("http-proxy-listen") && proxyListen == "" {
				return fail(c, "Provide a proxy authority or use --clear-http-proxy-listen.")
			}
			if clearProxy || c.Flags().Changed("http-proxy-listen") {
				changes.HTTPProxyListen = &proxyListen
			}
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
			return writeOfflineProblem(c, options.Output, &controlclient.Problem{Code: "service_unavailable", Title: title, Exit: 7})
		}
		return writeServiceResult(c, options.Output, verb, result)
	}
	return command
}

func writeServiceResult(c *cobra.Command, mode controlclient.OutputMode, verb string, result service.Result) error {
	if mode == controlclient.OutputJSON {
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(c.OutOrStdout(), string(data))
		return err
	}
	var text strings.Builder
	field := func(label, value string) {
		fmt.Fprintf(&text, "%-14s %s\n", label+":", controlclient.TerminalSafePath(value))
	}
	if verb != "status" {
		field("Operation", verb)
	} else {
		field("Installed", fmt.Sprint(result.Installed))
	}
	field("LaunchAgent", result.Launchd)
	field("Readiness", result.Readiness)
	if verb == "status" {
		if s := result.Settings; s != nil {
			text.WriteString("\nSettings\n")
			field("Listen", s.Listen)
			proxy := s.HTTPProxyListen
			if proxy == "" {
				proxy = "disabled"
			}
			field("HTTP proxy", proxy)
			field("Allowed hosts", strings.Join(s.AllowedHosts, ", "))
			if s.LogLevel != "" {
				field("Log level", s.LogLevel)
			}
			if s.Output != "" {
				field("Serve output", s.Output)
			}
			if s.JSON {
				field("Serve output", "json")
			}
			text.WriteString("\nPaths\n")
			field("Binary", s.Binary)
			field("Data", s.DataDir)
		}
		field("Plist", result.Plist)
		if result.Stdout != "" {
			field("Stdout", result.Stdout)
		}
		if result.Stderr != "" {
			field("Stderr", result.Stderr)
		}
	}
	if result.Message != "" {
		fmt.Fprintf(&text, "\n%s\n", controlclient.TerminalSafePath(result.Message))
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(&text, "\nWarning: %s\n", controlclient.TerminalSafePath(warning))
	}
	if result.Launchd == "launch-accepted" {
		text.WriteString("Run `agent-gateway service status` to check readiness.\n")
	}
	_, err := fmt.Fprint(c.OutOrStdout(), text.String())
	return err
}
