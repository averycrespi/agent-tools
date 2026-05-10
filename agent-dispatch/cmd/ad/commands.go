package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func notImplemented(name string) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		return fmt.Errorf("%s is not implemented yet", name)
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
	Args:  cobra.ExactArgs(1),
	RunE:  notImplemented("status"),
}

var logsCmd = &cobra.Command{
	Use:   "logs <task-id>",
	Short: "Show task logs",
	Args:  cobra.ExactArgs(1),
	RunE:  notImplemented("logs"),
}

var eventsCmd = &cobra.Command{
	Use:   "events <task-id>",
	Short: "Show task events",
	Args:  cobra.ExactArgs(1),
	RunE:  notImplemented("events"),
}

var attachCmd = &cobra.Command{
	Use:   "attach <task-id>",
	Short: "Attach to a read-only task monitor",
	Args:  cobra.ExactArgs(1),
	RunE:  notImplemented("attach"),
}

var steerCmd = &cobra.Command{
	Use:   "steer <task-id> <message>",
	Short: "Send steering to a running task",
	Args:  cobra.ExactArgs(2),
	RunE:  notImplemented("steer"),
}

var followupCmd = &cobra.Command{
	Use:     "followup <task-id> <message>",
	Aliases: []string{"follow-up"},
	Short:   "Queue a follow-up for a running task",
	Args:    cobra.ExactArgs(2),
	RunE:    notImplemented("followup"),
}

var followUpCmd = &cobra.Command{
	Use:    "follow-up <task-id> <message>",
	Hidden: true,
	Args:   cobra.ExactArgs(2),
	RunE:   notImplemented("follow-up"),
}

var stopCmd = &cobra.Command{
	Use:   "stop <task-id>",
	Short: "Stop a running task",
	Args:  cobra.ExactArgs(1),
	RunE:  notImplemented("stop"),
}

var rmCmd = &cobra.Command{
	Use:   "rm <task-id>",
	Short: "Remove task metadata and logs",
	Args:  cobra.ExactArgs(1),
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

var supervisorCmd = &cobra.Command{
	Use:    "supervisor",
	Hidden: true,
	RunE:   notImplemented("supervisor"),
}

func init() {
	logsCmd.Flags().BoolP("follow", "f", false, "follow log output")
	stopCmd.Flags().Bool("force", false, "force termination after graceful abort")
	rmCmd.Flags().Bool("worktree", false, "also remove the associated worktree")
	templateCmd.AddCommand(templateListCmd)
}
