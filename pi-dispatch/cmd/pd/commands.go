package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func notImplemented(name string) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		return fmt.Errorf("%s is not implemented yet", name)
	}
}

func exactArgs(names ...string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == len(names) {
			return nil
		}

		var message string
		if len(args) < len(names) {
			missing := names[len(args):]
			if len(missing) == 1 {
				message = fmt.Sprintf("%s is required", missing[0])
			} else {
				message = fmt.Sprintf("missing required arguments: %s are required", strings.Join(missing, " and "))
			}
		} else {
			message = fmt.Sprintf("too many arguments: expected %d, received %d", len(names), len(args))
		}

		message += fmt.Sprintf("\nUsage: %s", cmd.UseLine())
		if cmd.Example != "" {
			message += fmt.Sprintf("\nExample: %s", strings.TrimSpace(cmd.Example))
		}
		return errors.New(message)
	}
}

var runCmd = &cobra.Command{
	Use:   "run [prompt]",
	Short: "Start a background agent task",
	Args:  cobra.ArbitraryArgs,
	RunE:  notImplemented("run"),
}

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ps"},
	Short:   "List tasks",
	Args:    cobra.NoArgs,
	RunE:    notImplemented("list"),
}

var statusCmd = &cobra.Command{
	Use:   "status <task-id>",
	Short: "Show task status",
	Args:  exactArgs("task-id"),
	RunE:  notImplemented("status"),
}

var logsCmd = &cobra.Command{
	Use:   "logs <task-id>",
	Short: "Show task logs",
	Args:  exactArgs("task-id"),
	RunE:  notImplemented("logs"),
}

var eventsCmd = &cobra.Command{
	Use:   "events <task-id>",
	Short: "Show task events",
	Args:  exactArgs("task-id"),
	RunE:  notImplemented("events"),
}

var attachCmd = &cobra.Command{
	Use:   "attach <task-id>",
	Short: "Attach to a read-only task monitor",
	Args:  exactArgs("task-id"),
	RunE:  notImplemented("attach"),
}

var steerCmd = &cobra.Command{
	Use:     "steer <task-id> <message>",
	Short:   "Send steering to a running task",
	Example: `pd steer task-123 "focus on the failing package"`,
	Args:    exactArgs("task-id", "message"),
	RunE:    notImplemented("steer"),
}

var followupCmd = &cobra.Command{
	Use:     "followup <task-id> <message>",
	Aliases: []string{"follow-up"},
	Short:   "Queue a follow-up for a running task",
	Example: `pd followup task-123 "run the full test suite now"`,
	Args:    exactArgs("task-id", "message"),
	RunE:    notImplemented("followup"),
}

var stopCmd = &cobra.Command{
	Use:   "stop <task-id>",
	Short: "Stop a running task",
	Args:  exactArgs("task-id"),
	RunE:  notImplemented("stop"),
}

var rmCmd = &cobra.Command{
	Use:   "rm <task-id>",
	Short: "Remove task metadata and logs",
	Args:  exactArgs("task-id"),
	RunE:  notImplemented("rm"),
}

var templateCmd = &cobra.Command{
	Use:   "template",
	Short: "Manage templates",
}

var templateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List templates",
	Args:  cobra.NoArgs,
	RunE:  notImplemented("template list"),
}

var templateValidateCmd = &cobra.Command{
	Use:   "validate [template]",
	Short: "Validate one or all templates",
	Args:  cobra.MaximumNArgs(1),
	RunE:  notImplemented("template validate"),
}

var templateShowCmd = &cobra.Command{
	Use:   "show <template>",
	Short: "Show a template",
	Args:  exactArgs("template"),
	RunE:  notImplemented("template show"),
}

var templateRenderCmd = &cobra.Command{
	Use:   "render <template>",
	Short: "Render a template to Pi argv",
	Args:  exactArgs("template"),
	RunE:  notImplemented("template render"),
}

var supervisorCmd = &cobra.Command{
	Use:    "supervisor",
	Hidden: true,
	RunE:   notImplemented("supervisor"),
}

func init() {
	logsCmd.Flags().BoolP("follow", "f", false, "follow log output")
	stopCmd.Flags().Bool("force", false, "force termination after graceful abort")
	rmCmd.Flags().Bool("worktree", false, "also remove the associated worktree")
	templateCmd.AddCommand(templateListCmd, templateValidateCmd, templateShowCmd, templateRenderCmd)
}
