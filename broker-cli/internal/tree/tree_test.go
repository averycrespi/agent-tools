package tree_test

import (
	"testing"

	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
	"github.com/averycrespi/agent-tools/broker-cli/internal/tree"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func findCommand(root *cobra.Command, path ...string) *cobra.Command {
	cmd := root
	for _, name := range path {
		sub, _, err := cmd.Find([]string{name})
		if err != nil || sub == cmd {
			return nil
		}
		cmd = sub
	}
	return cmd
}

func TestBuild_singleTool(t *testing.T) {
	root := &cobra.Command{Use: "broker-cli"}
	tools := []client.Tool{
		{Name: "git.push", Description: "Push commits"},
	}
	tree.Build(root, tools, nil)

	ns := findCommand(root, "git")
	require.NotNil(t, ns, "expected 'git' namespace command")

	cmd := findCommand(root, "git", "push")
	require.NotNil(t, cmd, "expected 'git push' command")
	assert.Equal(t, "Push commits", cmd.Short)
}

func TestBuild_underscoreNormalized(t *testing.T) {
	root := &cobra.Command{Use: "broker-cli"}
	tools := []client.Tool{
		{Name: "github.list_prs", Description: "List PRs"},
	}
	tree.Build(root, tools, nil)

	cmd := findCommand(root, "github", "list-prs")
	require.NotNil(t, cmd, "expected 'github list-prs' command (kebab-case)")
}

func TestBuild_multipleNamespaces(t *testing.T) {
	root := &cobra.Command{Use: "broker-cli"}
	tools := []client.Tool{
		{Name: "git.push", Description: "Push"},
		{Name: "git.pull", Description: "Pull"},
		{Name: "github.list_prs", Description: "List PRs"},
	}
	tree.Build(root, tools, nil)

	assert.NotNil(t, findCommand(root, "git", "push"))
	assert.NotNil(t, findCommand(root, "git", "pull"))
	assert.NotNil(t, findCommand(root, "github", "list-prs"))
}

func TestBuild_namespaceHelp_listsTools(t *testing.T) {
	root := &cobra.Command{Use: "broker-cli"}
	tools := []client.Tool{
		{Name: "git.push", Description: "Push commits"},
		{Name: "git.pull", Description: "Pull changes"},
	}
	tree.Build(root, tools, nil)

	ns := findCommand(root, "git")
	require.NotNil(t, ns)
	cmds := ns.Commands()
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = c.Name()
	}
	assert.Contains(t, names, "push")
	assert.Contains(t, names, "pull")
}

func TestBuild_execFnCalled(t *testing.T) {
	root := &cobra.Command{Use: "broker-cli"}
	var calledWith struct {
		tool string
		args map[string]any
	}
	exec := func(tool string, args map[string]any) error {
		calledWith.tool = tool
		calledWith.args = args
		return nil
	}

	tools := []client.Tool{
		{Name: "git.push", Description: "Push", InputSchema: map[string]any{}},
	}
	tree.Build(root, tools, exec)

	cmd := findCommand(root, "git", "push")
	require.NotNil(t, cmd)
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Equal(t, "git.push", calledWith.tool)
}
