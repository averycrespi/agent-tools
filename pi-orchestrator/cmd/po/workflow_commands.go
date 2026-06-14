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
			path, err := existingWorkflowFilePath(name)
			if err != nil {
				return err
			}
			if _, err := workflow.LoadFile(path); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), name); err != nil {
				return err
			}
		}
		return nil
	},
}

var showCmd = &cobra.Command{
	Use:   "show <workflow>",
	Short: "Show a workflow definition",
	Args:  requireWorkflowArg("po show <workflow>"),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := workflowFilePath(args[0])
		if err != nil {
			return err
		}
		if _, err := workflow.LoadFile(path); err != nil {
			return err
		}
		data, err := os.ReadFile(path) // #nosec G304 -- path is resolved under the configured workflow definition directory.
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
		path, err := workflowFilePath(args[0])
		if err != nil {
			return err
		}
		if _, err := workflow.LoadFile(path); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s ok\n", args[0])
		return err
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

func workflowFilePath(name string) (string, error) {
	if strings.ContainsAny(name, `/\\`) {
		return "", fmt.Errorf("workflow name must not contain path separators: %s", name)
	}
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if stem == "" || stem == "." || stem == ".." {
		return "", fmt.Errorf("workflow name is invalid: %s", name)
	}
	if filepath.Ext(name) == ".yaml" || filepath.Ext(name) == ".yml" {
		return filepath.Join(resolveWorkflowDir(), name), nil
	}
	return filepath.Join(resolveWorkflowDir(), name+".yaml"), nil
}

func existingWorkflowFilePath(name string) (string, error) {
	path, err := workflowFilePath(name)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil || filepath.Ext(name) != "" {
		return path, err
	}
	ymlPath := strings.TrimSuffix(path, ".yaml") + ".yml"
	if _, err := os.Stat(ymlPath); err == nil {
		return ymlPath, nil
	}
	return path, nil
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
