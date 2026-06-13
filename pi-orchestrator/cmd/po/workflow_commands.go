package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/workflow"
	"github.com/spf13/cobra"
)

var workflowDir string

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List workflow definitions",
	RunE: func(cmd *cobra.Command, args []string) error {
		entries, err := os.ReadDir(resolveWorkflowDir())
		if err != nil {
			return fmt.Errorf("list workflows: %w", err)
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name, ok := workflowNameFromFile(entry.Name())
			if ok {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			if _, err := workflow.LoadFile(workflowFilePath(name)); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), name)
		}
		return nil
	},
}

var showCmd = &cobra.Command{
	Use:   "show <workflow>",
	Short: "Show a workflow definition",
	Args:  requireWorkflowArg("po show <workflow>"),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := workflowFilePath(args[0])
		if _, err := workflow.LoadFile(path); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read workflow %s: %w", path, err)
		}
		_, err = cmd.OutOrStdout().Write(data)
		return err
	},
}

var lintCmd = &cobra.Command{
	Use:   "lint <workflow>",
	Short: "Validate a workflow definition",
	Args:  requireWorkflowArg("po lint <workflow>"),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := workflow.LoadFile(workflowFilePath(args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s ok\n", args[0])
		return nil
	},
}

func requireWorkflowArg(usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("workflow is required\nUsage: %s", usage)
		}
		if len(args) > 1 {
			return fmt.Errorf("expected one workflow\nUsage: %s", usage)
		}
		return nil
	}
}

func workflowFilePath(name string) string {
	if filepath.Ext(name) == ".yaml" || filepath.Ext(name) == ".yml" {
		return filepath.Join(resolveWorkflowDir(), name)
	}
	return filepath.Join(resolveWorkflowDir(), name+".yaml")
}

func workflowNameFromFile(name string) (string, bool) {
	ext := filepath.Ext(name)
	if ext != ".yaml" && ext != ".yml" {
		return "", false
	}
	return strings.TrimSuffix(name, ext), true
}

func resolveWorkflowDir() string {
	return cfg.WorkflowDir
}
