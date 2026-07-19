package actions

import (
	"context"
	"crypto/rand"
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
	Manual   Trigger = "manual"
	Explicit Trigger = "explicit"
	Passive  Trigger = "passive"
)

type Worktree struct {
	Path     string
	Identity string
}
type Result struct {
	Skipped bool
	Reason  string
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

func DefinitionKind(repo config.Repository, definitionDigest string) string {
	switch definitionDigest {
	case digest(repo):
		return "setup"
	case digestValue(struct{ Launch string }{repo.LaunchCommand}):
		return "launch"
	default:
		return "unknown"
	}
}

func policyAllows(policy config.ActionPolicy, trigger Trigger) bool {
	switch trigger {
	case Manual:
		return policy != config.ActionNone
	case Explicit:
		return policy == config.ActionWTSCreated || policy == config.ActionAll
	case Passive:
		return policy == config.ActionAll
	default:
		return false
	}
}

func ledgerTrigger(trigger Trigger) string {
	if trigger == Manual {
		return string(Explicit)
	}
	return string(trigger)
}

func (m *Manager) Run(ctx context.Context, repo config.Repository, worktree Worktree, trigger Trigger, rerun bool) Result {
	enabled := policyAllows(repo.SetupPolicy, trigger)
	if !enabled || (len(repo.CopyActions) == 0 && len(repo.SetupActions) == 0) {
		return Result{Skipped: true}
	}
	key := state.ActionKey{Repository: repo.Identity(), Worktree: worktree.Identity, Trigger: ledgerTrigger(trigger), Digest: digest(repo)}
	return m.attempt(ctx, key, rerun, func() error { return m.execute(ctx, repo, worktree) })
}

func (m *Manager) Launch(ctx context.Context, repo config.Repository, worktree Worktree, trigger Trigger, rerun bool, launch func() error) Result {
	enabled := policyAllows(repo.LaunchPolicy, trigger)
	if !enabled {
		return Result{Skipped: true, Reason: "disabled by policy"}
	}
	if repo.LaunchCommand == "" {
		return Result{Skipped: true, Reason: "no launch command is configured"}
	}
	key := state.ActionKey{Repository: repo.Identity(), Worktree: worktree.Identity, Trigger: ledgerTrigger(trigger), Digest: digestValue(struct{ Launch string }{repo.LaunchCommand})}
	return m.attempt(ctx, key, rerun, launch)
}

func (m *Manager) attempt(ctx context.Context, key state.ActionKey, rerun bool, operation func() error) Result {
	lock, err := state.Acquire(ctx, m.ledgerPath+".lock")
	if err != nil {
		return Result{Error: err}
	}
	defer func() { _ = lock.Unlock() }()
	ledger, err := state.LoadLedger(m.ledgerPath)
	if err != nil {
		return Result{Error: err}
	}
	if rerun {
		ledger.Rerun(key)
	}
	if !ledger.Eligible(key) {
		return Result{Skipped: true, Reason: "already attempted"}
	}
	err = operation()
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

func (m *Manager) Prune(ctx context.Context, repo config.Repository, live map[string]bool) error {
	lock, err := state.Acquire(ctx, m.ledgerPath+".lock")
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	ledger, err := state.LoadLedger(m.ledgerPath)
	if err != nil {
		return err
	}
	validDigests := map[string]bool{digest(repo): true, digestValue(struct{ Launch string }{repo.LaunchCommand}): true}
	changed := false
	for encoded := range ledger.Attempts {
		key, decodeErr := state.DecodeActionKey(encoded)
		if decodeErr != nil {
			return decodeErr
		}
		if key.Repository == repo.Identity() && (!live[key.Worktree] || !validDigests[key.Digest]) {
			delete(ledger.Attempts, encoded)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return ledger.Save(m.ledgerPath)
}

func (m *Manager) execute(ctx context.Context, repo config.Repository, worktree Worktree) error {
	for _, action := range repo.CopyActions {
		if err := copyFile(repo, worktree.Path, action); err != nil {
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
		_, err := m.runner.RunEnv(commandCtx, worktree.Path, environment, action.Argv[0], action.Argv[1:]...)
		cancel()
		if err != nil {
			return fmt.Errorf("setup %q failed: %w", action.Argv[0], err)
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

func copyFile(repo config.Repository, worktree string, action config.CopyAction) error {
	sourceRelative, err := safeRelative(action.Source)
	if err != nil {
		return err
	}
	destinationRelative, err := safeRelative(action.Destination)
	if err != nil {
		return err
	}
	sourceRoot, err := os.OpenRoot(repo.PrimaryRoot)
	if err != nil {
		return fmt.Errorf("opening registered primary root: %w", err)
	}
	defer func() { _ = sourceRoot.Close() }()
	input, err := sourceRoot.Open(sourceRelative)
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
	destinationRoot, err := openWorktreeRoot(repo.AllowedRoots, worktree)
	if err != nil {
		return fmt.Errorf("opening worktree root: %w", err)
	}
	defer func() { _ = destinationRoot.Close() }()
	if parent := filepath.Dir(destinationRelative); parent != "." {
		if err := destinationRoot.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("creating copy destination directory: %w", err)
		}
	}
	temporary, err := temporaryName(filepath.Dir(destinationRelative))
	if err != nil {
		return err
	}
	output, err := destinationRoot.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm()) //nolint:gosec // permissions intentionally follow trusted configured source
	if err != nil {
		return fmt.Errorf("creating temporary copy destination: %w", err)
	}
	defer func() { _ = destinationRoot.Remove(temporary) }()
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return fmt.Errorf("copying file: %w", err)
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return fmt.Errorf("syncing copied file: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("closing copied file: %w", err)
	}
	if err := destinationRoot.Link(temporary, destinationRelative); errors.Is(err, os.ErrExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("publishing copied file: %w", err)
	}
	if err := destinationRoot.Remove(temporary); err != nil {
		return fmt.Errorf("removing temporary copied file: %w", err)
	}
	return nil
}

func openWorktreeRoot(allowedRoots []string, worktree string) (*os.Root, error) {
	for _, allowed := range allowedRoots {
		if !config.Contains(allowed, worktree) {
			continue
		}
		root, err := os.OpenRoot(allowed)
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(allowed, worktree)
		if err != nil {
			_ = root.Close()
			return nil, err
		}
		worktreeRoot, err := root.OpenRoot(relative)
		_ = root.Close()
		return worktreeRoot, err
	}
	return nil, fmt.Errorf("worktree is outside configured allowed roots")
}

func temporaryName(parent string) (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("creating temporary copy name: %w", err)
	}
	return filepath.Join(parent, ".wts-copy-"+hex.EncodeToString(random[:])), nil
}
