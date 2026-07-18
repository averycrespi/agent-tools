package actions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
)

type Trigger string

const (
	Explicit Trigger = "explicit"
	Passive  Trigger = "passive"
)

type Worktree struct {
	Path     string
	Identity string
}
type Result struct {
	Skipped bool
	Error   error
}

type runner interface {
	RunEnv(context.Context, string, []string, string, ...string) ([]byte, error)
}

type Manager struct {
	runner     runner
	ledgerPath string
	timeout    time.Duration
}

func New(runner runner, ledgerPath string, timeout time.Duration) *Manager {
	return &Manager{runner: runner, ledgerPath: ledgerPath, timeout: timeout}
}

func digestValue(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func digest(repo config.Repository) string {
	return digestValue(struct {
		Copies []config.CopyAction
		Setup  []config.SetupAction
	}{repo.CopyActions, repo.SetupActions})
}

func (m *Manager) Run(ctx context.Context, repo config.Repository, worktree Worktree, trigger Trigger, rerun bool) Result {
	enabled := (trigger == Explicit && repo.Policy.SetupExplicit) || (trigger == Passive && repo.Policy.SetupPassive)
	if !enabled || (len(repo.CopyActions) == 0 && len(repo.SetupActions) == 0) {
		return Result{Skipped: true}
	}
	ledger, err := state.LoadLedger(m.ledgerPath)
	if err != nil {
		return Result{Error: err}
	}
	key := state.ActionKey{Repository: repo.RepositoryIdentity, Worktree: worktree.Identity, Trigger: string(trigger), Digest: digest(repo)}
	if rerun {
		ledger.Rerun(key)
	}
	if !ledger.Eligible(key) {
		return Result{Skipped: true}
	}
	err = m.execute(ctx, repo, worktree)
	result := state.ActionResult{Success: err == nil}
	if err != nil {
		result.Error = err.Error()
	}
	ledger.Record(key, result)
	if saveErr := ledger.Save(m.ledgerPath); saveErr != nil {
		err = errors.Join(err, saveErr)
	}
	return Result{Error: err}
}

func (m *Manager) Launch(repo config.Repository, worktree Worktree, trigger Trigger, rerun bool, launch func() error) Result {
	enabled := (trigger == Explicit && repo.Policy.LaunchExplicit) || (trigger == Passive && repo.Policy.LaunchPassive)
	if !enabled || repo.LaunchCommand == "" {
		return Result{Skipped: true}
	}
	ledger, err := state.LoadLedger(m.ledgerPath)
	if err != nil {
		return Result{Error: err}
	}
	key := state.ActionKey{Repository: repo.RepositoryIdentity, Worktree: worktree.Identity, Trigger: string(trigger), Digest: digestValue(struct{ Launch string }{repo.LaunchCommand})}
	if rerun {
		ledger.Rerun(key)
	}
	if !ledger.Eligible(key) {
		return Result{Skipped: true}
	}
	err = launch()
	result := state.ActionResult{Success: err == nil}
	if err != nil {
		result.Error = err.Error()
	}
	ledger.Record(key, result)
	if saveErr := ledger.Save(m.ledgerPath); saveErr != nil {
		err = errors.Join(err, saveErr)
	}
	return Result{Error: err}
}

func (m *Manager) execute(ctx context.Context, repo config.Repository, worktree Worktree) error {
	for _, action := range repo.CopyActions {
		if err := copyFile(repo.PrimaryRoot, worktree.Path, action); err != nil {
			return err
		}
	}
	for _, action := range repo.SetupActions {
		timeout := m.timeout
		if action.Timeout != "" {
			parsed, err := time.ParseDuration(action.Timeout)
			if err != nil {
				return err
			}
			timeout = parsed
		}
		commandCtx := ctx
		cancel := func() {}
		if timeout > 0 {
			commandCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		environment := mergeEnvironment(os.Environ(), action.Env)
		output, err := m.runner.RunEnv(commandCtx, worktree.Path, environment, action.Argv[0], action.Argv[1:]...)
		cancel()
		if err != nil {
			return fmt.Errorf("setup %q failed: %w: %s", action.Argv[0], err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func mergeEnvironment(base []string, overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func safeRelative(value string) (string, error) {
	if value == "" || filepath.IsAbs(value) {
		return "", fmt.Errorf("path %q must be non-empty and relative", value)
	}
	clean := filepath.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes its registered root", value)
	}
	return clean, nil
}

func ensureParent(root, relative string) (string, error) {
	canonicalRoot, err := config.CanonicalExisting(root)
	if err != nil {
		return "", err
	}
	current := canonicalRoot
	parts := strings.Split(filepath.Dir(relative), string(filepath.Separator))
	for _, part := range parts {
		if part == "." || part == "" {
			continue
		}
		next := filepath.Join(current, part)
		info, statErr := os.Lstat(next)
		if errors.Is(statErr, os.ErrNotExist) {
			if err := os.Mkdir(next, 0o700); err != nil {
				return "", fmt.Errorf("creating copy destination directory: %w", err)
			}
			current = next
			continue
		}
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, resolveErr := config.CanonicalExisting(next)
			if resolveErr != nil || !config.Contains(canonicalRoot, resolved) {
				return "", fmt.Errorf("copy destination symlink %q escapes worktree", next)
			}
			current = resolved
			continue
		}
		if !info.IsDir() {
			return "", fmt.Errorf("copy destination parent %q is not a directory", next)
		}
		current = next
	}
	return filepath.Join(canonicalRoot, relative), nil
}

func copyFile(primary, worktree string, action config.CopyAction) error {
	sourceRelative, err := safeRelative(action.Source)
	if err != nil {
		return err
	}
	destinationRelative, err := safeRelative(action.Destination)
	if err != nil {
		return err
	}
	canonicalPrimary, err := config.CanonicalExisting(primary)
	if err != nil {
		return err
	}
	source, err := filepath.EvalSymlinks(filepath.Join(canonicalPrimary, sourceRelative))
	if err != nil {
		return fmt.Errorf("resolving copy source: %w", err)
	}
	if !config.Contains(canonicalPrimary, source) {
		return fmt.Errorf("copy source %q escapes registered primary root", action.Source)
	}
	input, err := os.Open(source) //nolint:gosec // path is constrained to registered primary root
	if err != nil {
		return fmt.Errorf("opening copy source: %w", err)
	}
	defer func() { _ = input.Close() }()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("copy source %q is not a regular file", action.Source)
	}
	destination, err := ensureParent(worktree, destinationRelative)
	if err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm()) //nolint:gosec // permissions intentionally follow trusted configured source
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("creating copy destination: %w", err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		_ = os.Remove(destination)
		return fmt.Errorf("copying file: %w", err)
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}
