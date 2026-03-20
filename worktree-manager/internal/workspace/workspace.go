package workspace

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/worktree-manager/internal/config"
	"github.com/averycrespi/agent-tools/worktree-manager/internal/exec"
	"github.com/averycrespi/agent-tools/worktree-manager/internal/git"
)

// gitClient defines the git operations needed by the workspace service.
type gitClient interface {
	RepoInfo(path string) (git.Info, error)
	AddWorktree(repoRoot, worktreeDir, branch string) error
	RemoveWorktree(repoRoot, worktreeDir string) error
	DeleteBranch(repoRoot, branch string, force bool) error
	CommonDir(path string) (string, error)
}

// tmuxClient defines the tmux operations needed by the workspace service.
type tmuxClient interface {
	SessionExists(session string) bool
	CreateSession(session, window string) error
	CreateWindow(session, window, dir string) error
	KillWindow(session, window string) error
	WindowExists(session, window string) bool
	SendKeys(session, window, keys string) error
	Attach(session string) error
	AttachToWindow(session, window string) error
}

// Service manages workspace lifecycle.
type Service struct {
	git    gitClient
	tmux   tmuxClient
	config config.Config
	logger *slog.Logger
	runner exec.Runner
}

// NewService returns a workspace Service.
func NewService(g gitClient, t tmuxClient, cfg config.Config, l *slog.Logger, r exec.Runner) *Service {
	return &Service{git: g, tmux: t, config: cfg, logger: l, runner: r}
}

// Init ensures a tmux session exists for the repository.
func (s *Service) Init(repoRoot string) error {
	info, err := s.git.RepoInfo(repoRoot)
	if err != nil {
		return err
	}
	if info.IsWorktree {
		return fmt.Errorf("this command must be run from the main git repository, not a worktree")
	}

	tmuxSession := config.TmuxSessionName(info.Name)
	if s.tmux.SessionExists(tmuxSession) {
		s.logger.Debug("tmux session already exists", "session", tmuxSession)
		return nil
	}

	s.logger.Info("creating tmux session", "session", tmuxSession)
	return s.tmux.CreateSession(tmuxSession, "main")
}

// Add creates a new workspace: worktree, tmux window, config-driven setup, and optional launch command.
func (s *Service) Add(repoRoot, branch string) error {
	info, err := s.git.RepoInfo(repoRoot)
	if err != nil {
		return err
	}
	if info.IsWorktree {
		return fmt.Errorf("this command must be run from the main git repository, not a worktree")
	}

	if err := s.Init(repoRoot); err != nil {
		return err
	}

	tmuxSession := config.TmuxSessionName(info.Name)
	windowName := config.TmuxWindowName(branch)
	worktreeDir := config.WorktreeDir(info.Name, branch)

	// Create worktree if it doesn't exist
	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		s.logger.Info("creating worktree", "path", worktreeDir)
		if err := os.MkdirAll(filepath.Dir(worktreeDir), 0o755); err != nil { //nolint:gosec // 0755 is appropriate for worktree directories
			return fmt.Errorf("could not create worktree directory: %w", err)
		}
		if err := s.git.AddWorktree(info.Root, worktreeDir, branch); err != nil {
			return err
		}

		// Copy configured files from main repo to worktree
		for _, relPath := range s.config.CopyFiles {
			s.copyFile(info.Root, worktreeDir, relPath)
		}

		// Run configured setup scripts in the worktree
		s.runSetupScripts(worktreeDir)
	} else {
		s.logger.Debug("worktree already exists", "path", worktreeDir)
	}

	// Create tmux window if it doesn't exist
	if s.tmux.WindowExists(tmuxSession, windowName) {
		s.logger.Debug("tmux window already exists", "window", windowName)
	} else {
		s.logger.Info("creating tmux window", "window", windowName)
		if err := s.tmux.CreateWindow(tmuxSession, windowName, worktreeDir); err != nil {
			return err
		}
		if s.config.LaunchCommand != "" {
			s.logger.Info("sending launch command", "command", s.config.LaunchCommand)
			if err := s.tmux.SendKeys(tmuxSession, windowName, s.config.LaunchCommand); err != nil {
				return err
			}
		}
	}

	return nil
}

// Remove removes a workspace: worktree, tmux window, and optionally the branch.
func (s *Service) Remove(repoRoot, branch string, deleteBranch, forceDelete bool) error {
	info, err := s.git.RepoInfo(repoRoot)
	if err != nil {
		return err
	}
	if info.IsWorktree {
		return fmt.Errorf("this command must be run from the main git repository, not a worktree")
	}

	tmuxSession := config.TmuxSessionName(info.Name)
	windowName := config.TmuxWindowName(branch)
	worktreeDir := config.WorktreeDir(info.Name, branch)

	// Remove worktree if it exists
	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		s.logger.Debug("worktree does not exist", "path", worktreeDir)
	} else {
		s.logger.Info("removing worktree", "path", worktreeDir)
		if err := s.git.RemoveWorktree(info.Root, worktreeDir); err != nil {
			return err
		}
	}

	// Close tmux window if it exists
	switch {
	case !s.tmux.SessionExists(tmuxSession):
		s.logger.Debug("tmux session does not exist", "session", tmuxSession)
	case s.tmux.WindowExists(tmuxSession, windowName):
		s.logger.Info("closing tmux window", "window", windowName)
		if err := s.tmux.KillWindow(tmuxSession, windowName); err != nil {
			return err
		}
	default:
		s.logger.Debug("tmux window does not exist", "window", windowName)
	}

	// Delete branch if requested
	if deleteBranch || forceDelete {
		s.logger.Info("deleting branch", "branch", branch, "force", forceDelete)
		if err := s.git.DeleteBranch(info.Root, branch, forceDelete); err != nil {
			return err
		}
	}

	return nil
}

// Attach attaches to the tmux session for the repository at the given path.
// If branch is non-empty, attaches to the specific window for that branch.
// Works from both the main repo and worktrees.
func (s *Service) Attach(path, branch string) error {
	info, err := s.git.RepoInfo(path)
	if err != nil {
		return err
	}

	var repoName string
	if info.IsWorktree {
		commonDir, err := s.git.CommonDir(path)
		if err != nil {
			return fmt.Errorf("could not determine main repo: %w", err)
		}
		resolved := filepath.Clean(filepath.Join(path, commonDir))
		mainRoot := filepath.Dir(resolved)
		repoName = filepath.Base(mainRoot)
	} else {
		repoName = info.Name
	}

	tmuxSession := config.TmuxSessionName(repoName)

	if !s.tmux.SessionExists(tmuxSession) {
		if info.IsWorktree {
			return fmt.Errorf("tmux session does not exist: %s. Run 'wt add <branch>' from the main repository first", tmuxSession)
		}
		if err := s.Init(path); err != nil {
			return err
		}
	}

	if branch != "" {
		windowName := config.TmuxWindowName(branch)
		if !s.tmux.WindowExists(tmuxSession, windowName) {
			return fmt.Errorf("tmux window does not exist for branch: %s", branch)
		}
		s.logger.Info("attaching to tmux window", "session", tmuxSession, "window", windowName)
		return s.tmux.AttachToWindow(tmuxSession, windowName)
	}

	s.logger.Info("attaching to tmux session", "session", tmuxSession)
	return s.tmux.Attach(tmuxSession)
}

// runSetupScripts runs configured setup scripts in the worktree directory.
func (s *Service) runSetupScripts(worktreeDir string) {
	if s.runner == nil {
		return
	}
	for _, script := range s.config.SetupScripts {
		scriptPath := filepath.Join(worktreeDir, script)
		fi, err := os.Stat(scriptPath)
		if err != nil || fi.IsDir() {
			s.logger.Debug("setup script not found, skipping", "script", script)
			continue
		}
		s.logger.Info("running setup script", "script", script)
		if _, err := s.runner.RunDir(worktreeDir, scriptPath); err != nil {
			s.logger.Warn("setup script failed", "script", script, "error", err)
		}
	}
}

// copyFile copies a single file from the main repo to the worktree.
// Paths are relative to the respective roots. Silently skips if source doesn't exist.
func (s *Service) copyFile(repoRoot, worktreeDir, relPath string) {
	src := filepath.Join(repoRoot, relPath)
	dst := filepath.Join(worktreeDir, relPath)

	srcFile, err := os.Open(src) //nolint:gosec // path is constructed from config, not user input
	if err != nil {
		s.logger.Debug("copy source not found, skipping", "path", relPath)
		return
	}
	defer srcFile.Close() //nolint:errcheck // best-effort close on read-only file

	if _, err := os.Stat(dst); err == nil {
		s.logger.Debug("copy destination already exists, skipping", "path", relPath)
		return
	}

	s.logger.Info("copying file to worktree", "path", relPath)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil { //nolint:gosec // 0755 is appropriate for worktree directories
		s.logger.Warn("could not create directory for copy", "path", relPath, "error", err)
		return
	}
	dstFile, err := os.Create(dst) //nolint:gosec // path is constructed from config, not user input
	if err != nil {
		s.logger.Warn("could not create destination file", "path", relPath, "error", err)
		return
	}
	defer dstFile.Close() //nolint:errcheck // best-effort close; errors caught by io.Copy
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		s.logger.Warn("copy failed", "path", relPath, "error", err)
	}
}
