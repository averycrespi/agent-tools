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

func atLeastOneArg(name string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) >= 1 {
			return nil
		}
		message := fmt.Sprintf("%s is required", name)
		message += fmt.Sprintf("\nUsage: %s", cmd.UseLine())
		if cmd.Example != "" {
			message += fmt.Sprintf("\nExample: %s", strings.TrimSpace(cmd.Example))
		}
		return errors.New(message)
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
	Short: "Dispatch an autonomous Pi task",
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

var waitCmd = &cobra.Command{
	Use:   "wait <task-id>",
	Short: "Wait for a task to finish",
	Args:  exactArgs("task-id"),
	RunE:  notImplemented("wait"),
}

var logsCmd = &cobra.Command{
	Use:   "logs <task-id>",
	Short: "Show task stdout and stderr",
	Args:  exactArgs("task-id"),
	RunE:  notImplemented("logs"),
}

var stopCmd = &cobra.Command{
	Use:   "stop <task-id>...",
	Short: "Stop one or more running tasks",
	Args:  atLeastOneArg("task-id"),
	RunE:  notImplemented("stop"),
}

var cleanupCmd = &cobra.Command{
	Use:   "cleanup <task-id>...",
	Short: "Remove task worktree resources",
	Args:  atLeastOneArg("task-id"),
	RunE:  notImplemented("cleanup"),
}

var rmCmd = &cobra.Command{
	Use:   "rm <task-id>...",
	Short: "Forget inactive task metadata and logs",
	Args:  atLeastOneArg("task-id"),
	RunE:  notImplemented("rm"),
}

var supervisorCmd = &cobra.Command{
	Use:    "supervisor",
	Hidden: true,
	RunE:   notImplemented("supervisor"),
}

func init() {
	waitCmd.Flags().Duration("timeout", 0, "maximum time to wait, such as 30s, 5m, or 1h; 0 waits forever")
	logsCmd.Flags().BoolP("follow", "f", false, "follow log output")
	stopCmd.Flags().Bool("force", false, "force termination after graceful abort")
}
