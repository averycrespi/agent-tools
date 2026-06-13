package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/workflow"
	wtworktree "github.com/averycrespi/agent-tools/worktree-manager/pkg/worktree"
	"github.com/spf13/cobra"
)

var runInputs []string

type worktreeClient interface {
	AddHeadlessWithOwnership(repoRoot, branch string) (string, bool, error)
	Remove(repoRoot, branch string) error
}

var (
	defaultNewWorktreeClient = func() (worktreeClient, error) { return wtworktree.New() }
	newWorktreeClient        = defaultNewWorktreeClient
	nowFunc                  = time.Now
	shortIDFunc              = randomShortID
)

var runCmd = &cobra.Command{
	Use:   "run <workflow>",
	Short: "Run a workflow",
	Args:  requireWorkflowArg("po run <workflow>"),
	RunE:  runWorkflow,
}

func init() {
	runCmd.Flags().StringArrayVar(&runInputs, "input", nil, "workflow input as key=value")
}

func runWorkflow(cmd *cobra.Command, args []string) error {
	definitionPath, err := workflowFilePath(args[0])
	if err != nil {
		return err
	}
	definitionYAML, err := os.ReadFile(definitionPath) // #nosec G304 -- path is resolved under the configured workflow definition directory.
	if err != nil {
		return err
	}
	definitionStem := strings.TrimSuffix(filepath.Base(definitionPath), filepath.Ext(definitionPath))
	definition, err := workflow.LoadBytes(definitionYAML, definitionStem, definitionPath)
	if err != nil {
		return err
	}
	rawInputs, err := parseInputAssignments(runInputs)
	if err != nil {
		return err
	}
	validatedInputs, err := definition.ValidateInputs(rawInputs)
	if err != nil {
		return err
	}
	repo, err := renderRepo(definition.Repo, validatedInputs)
	if err != nil {
		return err
	}
	if err := validateArtifactParent(cfg.ArtifactParentDir); err != nil {
		return err
	}

	now := nowFunc().UTC()
	idPart := shortIDFunc()
	runID := "po-" + now.Format("20060102-150405") + "-" + idPart
	requestID := runID + "-request"
	branch := "po/" + definition.Name + "-" + idPart

	wt, err := newWorktreeClient()
	if err != nil {
		return err
	}
	worktreePath, _, err := wt.AddHeadlessWithOwnership(repo, branch)
	if err != nil {
		return err
	}

	artifactRoot := filepath.Join(cfg.ArtifactParentDir, runID)
	if err := ensureArtifactRootOutsideWorktree(artifactRoot, worktreePath); err != nil {
		return err
	}
	logDir := cfg.RunLogDir(runID)
	if err := os.MkdirAll(artifactRoot, 0o750); err != nil {
		return fmt.Errorf("create artifact root: %w", err)
	}
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		return fmt.Errorf("create run log dir: %w", err)
	}

	inputsJSON, err := json.Marshal(validatedInputs)
	if err != nil {
		return fmt.Errorf("encode workflow inputs: %w", err)
	}
	definitionHash, err := hashFile(definitionPath)
	if err != nil {
		return err
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	req := store.RunRequest{ID: requestID, Workflow: definition.Name, InputsJSON: string(inputsJSON), Source: "cli", CreatedAt: now}
	run := store.WorkflowRun{ID: runID, RequestID: requestID, Workflow: definition.Name, DefinitionHash: definitionHash, DefinitionYAML: string(definitionYAML), InputsJSON: string(inputsJSON), Repo: repo, Branch: branch, WorktreePath: worktreePath, ArtifactRoot: artifactRoot, State: store.StateStarting, SupervisorLogPath: filepath.Join(logDir, "supervisor.log"), CreatedAt: now, UpdatedAt: now}
	if err := db.CreateRunRequestWithWorkflowRun(cmd.Context(), req, run); err != nil {
		return err
	}
	pid, err := startSupervisor(run.SupervisorLogPath, "--workflow-dir", cfg.WorkflowDir, "--run-id", runID)
	if err != nil {
		return err
	}
	if err := db.UpdateWorkflowRunSupervisorPID(cmd.Context(), runID, pid); err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), runID)
	return err
}

func ensureArtifactRootOutsideWorktree(artifactRoot, worktreePath string) error {
	absArtifactRoot, err := filepath.Abs(artifactRoot)
	if err != nil {
		return fmt.Errorf("resolve artifact root: %w", err)
	}
	absWorktree, err := filepath.Abs(worktreePath)
	if err != nil {
		return fmt.Errorf("resolve workflow worktree: %w", err)
	}
	rel, err := filepath.Rel(absWorktree, absArtifactRoot)
	if err != nil {
		return fmt.Errorf("compare artifact root and workflow worktree: %w", err)
	}
	if rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("artifact root %s must be outside workflow worktree %s", artifactRoot, worktreePath)
	}
	return nil
}

func parseInputAssignments(assignments []string) (map[string]string, error) {
	inputs := make(map[string]string, len(assignments))
	for _, assignment := range assignments {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("input must be key=value")
		}
		if _, exists := inputs[key]; exists {
			return nil, fmt.Errorf("input %s specified more than once", key)
		}
		inputs[key] = value
	}
	return inputs, nil
}

func renderRepo(repoTemplate string, inputs workflow.InputValues) (string, error) {
	tmpl, err := template.New("repo").Parse(repoTemplate)
	if err != nil {
		return "", fmt.Errorf("parse repo template: %w", err)
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, map[string]any{"Inputs": inputs}); err != nil {
		return "", fmt.Errorf("render repo template: %w", err)
	}
	repo := strings.TrimSpace(rendered.String())
	if repo == "" {
		return "", fmt.Errorf("rendered repo is empty")
	}
	return repo, nil
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is resolved under the configured workflow definition directory.
	if err != nil {
		return "", fmt.Errorf("read workflow for hash: %w", err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func randomShortID() string {
	var data [4]byte
	if _, err := rand.Read(data[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(data[:])
}
