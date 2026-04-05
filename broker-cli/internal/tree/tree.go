package tree

import (
	"strings"

	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
	"github.com/averycrespi/agent-tools/broker-cli/internal/flags"
	"github.com/spf13/cobra"
)

// ExecFn is called when a tool command runs. It receives the original
// dot-separated tool name (e.g. "git.push") and the parsed arguments.
type ExecFn func(tool string, args map[string]any) error

// Build populates root with a cobra command tree derived from tools.
// Tool names like "git.push" become "root git push".
// Underscores in tool names are normalized to hyphens.
func Build(root *cobra.Command, tools []client.Tool, exec ExecFn) {
	namespaces := make(map[string]*cobra.Command)

	for _, tool := range tools {
		parts := strings.SplitN(tool.Name, ".", 2)
		if len(parts) != 2 {
			continue
		}
		ns, toolName := parts[0], parts[1]
		cmdName := strings.ReplaceAll(toolName, "_", "-")

		// Get or create namespace command.
		nsCmd, ok := namespaces[ns]
		if !ok {
			nsCmd = &cobra.Command{
				Use:   ns,
				Short: ns,
			}
			namespaces[ns] = nsCmd
			root.AddCommand(nsCmd)
		}

		// Capture loop variables for closure.
		t := tool
		toolCmd := &cobra.Command{
			Use:          cmdName,
			Short:        t.Description,
			SilenceUsage: true,
		}

		flags.AddSchemaFlags(toolCmd, t.InputSchema)

		if exec != nil {
			toolCmd.RunE = func(cmd *cobra.Command, _ []string) error {
				args, err := flags.BuildArgs(cmd, t.InputSchema)
				if err != nil {
					return err
				}
				return exec(t.Name, args)
			}
		}

		nsCmd.AddCommand(toolCmd)
	}
}
