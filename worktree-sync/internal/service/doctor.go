package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	gitclient "github.com/averycrespi/agent-tools/worktree-sync/internal/git"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/launchd"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/reconcile"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/tmux"
)

type doctorStatus string

const (
	doctorOK      doctorStatus = "ok"
	doctorWarning doctorStatus = "warning"
	doctorError   doctorStatus = "error"
	doctorSkipped doctorStatus = "skipped"
)

type doctorCheck struct {
	ID       string       `json:"id"`
	Status   doctorStatus `json:"status"`
	Summary  string       `json:"summary"`
	Details  []string     `json:"details"`
	Recovery string       `json:"recovery"`
}

type doctorReport struct {
	Version int           `json:"version"`
	Checks  []doctorCheck `json:"checks"`
}

func doctorHasErrors(report doctorReport) bool {
	for _, check := range report.Checks {
		if check.Status == doctorError {
			return true
		}
	}
	return false
}

func renderDoctorHuman(report doctorReport) string {
	lines := make([]string, 0, len(report.Checks))
	for _, check := range report.Checks {
		lines = append(lines, fmt.Sprintf("%s %s: %s", check.Status, check.ID, check.Summary))
		for _, detail := range check.Details {
			lines = append(lines, "  "+detail)
		}
		if check.Recovery != "" {
			lines = append(lines, "  next: "+check.Recovery)
		}
	}
	return strings.Join(lines, "\n")
}

func newDoctorCheck(id string, status doctorStatus, summary, recovery string, details ...string) doctorCheck {
	if details == nil {
		details = []string{}
	}
	return doctorCheck{ID: id, Status: status, Summary: summary, Details: details, Recovery: recovery}
}

func (s *Service) doctorRun(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	commandCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		commandCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	return s.runner.Run(commandCtx, "", name, args...)
}

func doctorFileCheck(id, path string, absentOK bool) doctorCheck {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) && absentOK {
		return newDoctorCheck(id, doctorOK, "not created yet", "")
	}
	if err != nil {
		return newDoctorCheck(id, doctorError, "cannot access "+filepath.Base(path), "repair the private worktree-sync state path", err.Error())
	}
	if info.Mode().Perm()&0o077 != 0 {
		return newDoctorCheck(id, doctorError, "permissions are not private", "set owner-only permissions on "+path, info.Mode().Perm().String())
	}
	return newDoctorCheck(id, doctorOK, "readable with private permissions", "")
}

func (s *Service) collectDoctor(ctx context.Context) doctorReport {
	report := doctorReport{Version: 1, Checks: []doctorCheck{}}
	defaults := config.Default()
	timeout, _ := time.ParseDuration(defaults.Global.CommandTimeout)

	if output, err := s.doctorRun(ctx, timeout, "git", "--version"); err != nil {
		report.Checks = append(report.Checks, newDoctorCheck("tools.git", doctorError, "Git is unavailable", "install Git and ensure it is on PATH"))
	} else {
		report.Checks = append(report.Checks, newDoctorCheck("tools.git", doctorOK, strings.TrimSpace(string(output)), ""))
	}
	if output, err := s.doctorRun(ctx, timeout, "tmux", "-V"); err != nil {
		report.Checks = append(report.Checks, newDoctorCheck("tools.tmux", doctorError, "tmux is unavailable", "install tmux and ensure it is on PATH"))
	} else {
		report.Checks = append(report.Checks, newDoctorCheck("tools.tmux", doctorOK, strings.TrimSpace(string(output)), ""))
	}

	configHome, dataHome, stateHome := s.paths.XDGHomePaths()
	resolved := []string{configHome, dataHome, stateHome, s.paths.Config, s.paths.State, s.paths.Worktrees}
	pathsAbsolute := true
	for _, path := range resolved {
		pathsAbsolute = pathsAbsolute && filepath.IsAbs(path)
	}
	if pathsAbsolute {
		report.Checks = append(report.Checks, newDoctorCheck("paths.resolved", doctorOK, "XDG and worktree-sync paths are absolute", "", resolved...))
	} else {
		report.Checks = append(report.Checks, newDoctorCheck("paths.resolved", doctorError, "one or more resolved paths are not absolute", "set absolute XDG home paths", resolved...))
	}

	var cfg config.Config
	configValid := false
	diagnosticRepos := []config.Repository{}
	data, readErr := os.ReadFile(s.paths.Config) //nolint:gosec // fixed application config path
	configMissing := errors.Is(readErr, os.ErrNotExist)
	switch {
	case configMissing:
		report.Checks = append(report.Checks, newDoctorCheck("config.file", doctorOK, "configuration file is absent; defaults are active", ""))
		data, _ = json.Marshal(defaults)
	case readErr != nil:
		report.Checks = append(report.Checks, newDoctorCheck("config.file", doctorError, "configuration file is unreadable", "repair permissions or run wts config edit", readErr.Error()))
	case true:
		report.Checks = append(report.Checks, doctorFileCheck("config.file", s.paths.Config, false))
	}

	syntaxValid := readErr == nil || configMissing
	if syntaxValid && !json.Valid(data) {
		syntaxValid = false
	}
	if syntaxValid {
		report.Checks = append(report.Checks, newDoctorCheck("config.syntax", doctorOK, "configuration JSON syntax is valid", ""))
	} else {
		report.Checks = append(report.Checks, newDoctorCheck("config.syntax", doctorError, "configuration JSON syntax is invalid", "run wts config edit"))
	}
	versionValid := false
	if syntaxValid {
		var header struct {
			Version int `json:"version"`
		}
		if err := json.Unmarshal(data, &header); err == nil && header.Version == config.Version {
			versionValid = true
			report.Checks = append(report.Checks, newDoctorCheck("config.version", doctorOK, fmt.Sprintf("configuration version %d is current", header.Version), ""))
		} else {
			report.Checks = append(report.Checks, newDoctorCheck("config.version", doctorError, "configuration version is unsupported or requires migration", "run wts config refresh"))
		}
	} else {
		report.Checks = append(report.Checks, newDoctorCheck("config.version", doctorSkipped, "configuration version check requires valid JSON", ""))
	}
	if versionValid {
		diagnosticConfig, decodeErr := config.DecodeForDiagnostics(data)
		if configMissing {
			diagnosticConfig, decodeErr = defaults, nil
		}
		if decodeErr == nil {
			diagnosticRepos = append(diagnosticRepos, diagnosticConfig.Repositories...)
			sort.Slice(diagnosticRepos, func(i, j int) bool { return diagnosticRepos[i].ID < diagnosticRepos[j].ID })
		}
		loaded, err := config.Load(s.paths.Config)
		if configMissing {
			loaded, err = defaults, nil
		}
		if decodeErr != nil {
			err = decodeErr
		}
		if err != nil {
			report.Checks = append(report.Checks, newDoctorCheck("config.runtime", doctorError, "configuration fails runtime validation", "run wts config edit", err.Error()))
		} else {
			cfg, configValid = loaded, true
			if len(cfg.Repositories) == 0 {
				report.Checks = append(report.Checks, newDoctorCheck("config.runtime", doctorWarning, "configuration is valid but no repositories are registered", "run wts repo add"))
			} else {
				report.Checks = append(report.Checks, newDoctorCheck("config.runtime", doctorOK, "configuration passes runtime validation", ""))
			}
		}
	} else {
		report.Checks = append(report.Checks, newDoctorCheck("config.runtime", doctorSkipped, "runtime validation requires current valid configuration", ""))
	}

	stateInfo, stateErr := os.Stat(s.paths.State)
	switch {
	case errors.Is(stateErr, os.ErrNotExist):
		report.Checks = append(report.Checks, newDoctorCheck("state.directory", doctorOK, "state directory is not created yet", ""))
	case stateErr != nil:
		report.Checks = append(report.Checks, newDoctorCheck("state.directory", doctorError, "state directory is unavailable", "repair the private state directory", stateErr.Error()))
	case !stateInfo.IsDir() || stateInfo.Mode().Perm()&0o077 != 0:
		report.Checks = append(report.Checks, newDoctorCheck("state.directory", doctorError, "state path is not a private directory", "set owner-only directory permissions"))
	default:
		report.Checks = append(report.Checks, newDoctorCheck("state.directory", doctorOK, "state directory is private", ""))
	}
	ledgerPath := filepath.Join(s.paths.State, "actions.json")
	ledgerCheck := doctorFileCheck("state.action_ledger", ledgerPath, true)
	if ledgerCheck.Status == doctorOK {
		if _, err := state.LoadLedger(ledgerPath); err != nil {
			ledgerCheck = newDoctorCheck("state.action_ledger", doctorError, "action ledger is invalid", "back up and repair the action ledger", err.Error())
		}
	}
	report.Checks = append(report.Checks, ledgerCheck)
	provenancePath := filepath.Join(s.paths.State, "provenance.json")
	provenanceCheck := doctorFileCheck("state.provenance", provenancePath, true)
	if provenanceCheck.Status == doctorOK {
		if _, err := state.LoadProvenance(provenancePath); err != nil {
			provenanceCheck = newDoctorCheck("state.provenance", doctorError, "provenance state is invalid", "back up and repair provenance state", err.Error())
		}
	}
	report.Checks = append(report.Checks, provenanceCheck)

	gitSnapshots := make(map[string]gitclient.Snapshot)
	gitClient := gitclient.New(s.runner, timeout)
	if configValid {
		for _, repo := range cfg.Repositories {
			for _, item := range []struct{ suffix, path string }{{"primary", repo.PrimaryRoot}, {"common_git", repo.CommonGitDir}, {"creation_root", repo.WorktreeCreationRoot}} {
				if _, err := config.CanonicalExisting(item.path); err != nil {
					report.Checks = append(report.Checks, newDoctorCheck("repository."+repo.ID+"."+item.suffix, doctorError, item.path+" is unavailable", "repair the path or run wts config edit", err.Error()))
				} else {
					report.Checks = append(report.Checks, newDoctorCheck("repository."+repo.ID+"."+item.suffix, doctorOK, item.path+" is available", ""))
				}
			}
			for index, root := range repo.AllowedRoots {
				id := "repository." + repo.ID + ".allowed_root." + strconv.Itoa(index)
				if _, err := config.CanonicalExisting(root); err != nil {
					report.Checks = append(report.Checks, newDoctorCheck(id, doctorError, root+" is unavailable", "repair the path or remove the allowed root", err.Error()))
				} else {
					report.Checks = append(report.Checks, newDoctorCheck(id, doctorOK, root+" is available", ""))
				}
			}
			snapshot, err := gitClient.Snapshot(ctx, gitclient.Repository{PrimaryRoot: repo.PrimaryRoot, CommonGitDir: repo.CommonGitDir})
			gitSnapshots[repo.ID] = snapshot
			if err != nil || !snapshot.Complete {
				report.Checks = append(report.Checks, newDoctorCheck("repository."+repo.ID+".git_snapshot", doctorError, "Git worktree snapshot is unavailable or incomplete", "repair the registered repository metadata"))
			} else {
				report.Checks = append(report.Checks, newDoctorCheck("repository."+repo.ID+".git_snapshot", doctorOK, "Git worktree snapshot is complete", ""))
			}
		}
	} else {
		for _, repo := range diagnosticRepos {
			for _, suffix := range []string{"primary", "common_git", "creation_root"} {
				report.Checks = append(report.Checks, newDoctorCheck("repository."+repo.ID+"."+suffix, doctorSkipped, "repository check requires runtime-valid configuration", ""))
			}
			for index := range repo.AllowedRoots {
				report.Checks = append(report.Checks, newDoctorCheck("repository."+repo.ID+".allowed_root."+strconv.Itoa(index), doctorSkipped, "repository check requires runtime-valid configuration", ""))
			}
			report.Checks = append(report.Checks, newDoctorCheck("repository."+repo.ID+".git_snapshot", doctorSkipped, "Git snapshot requires runtime-valid configuration", ""))
		}
	}

	tmuxClient := tmux.New(s.runner, s.socket, timeout)
	tmuxSnapshot, tmuxErr := tmuxClient.Snapshot(ctx)
	switch {
	case tmuxErr != nil || !tmuxSnapshot.Complete:
		report.Checks = append(report.Checks, newDoctorCheck("tmux.snapshot", doctorError, "dedicated tmux snapshot is unavailable or incomplete", "check tmux and the dedicated wts socket"))
	case len(tmuxSnapshot.Sessions) == 0:
		report.Checks = append(report.Checks, newDoctorCheck("tmux.snapshot", doctorOK, "dedicated tmux server is not running yet", ""))
	default:
		report.Checks = append(report.Checks, newDoctorCheck("tmux.snapshot", doctorOK, "dedicated tmux snapshot is complete", ""))
	}
	if configValid {
		for _, repo := range cfg.Repositories {
			snapshot, ok := gitSnapshots[repo.ID]
			if tmuxErr != nil || !tmuxSnapshot.Complete || !ok || !snapshot.Complete {
				report.Checks = append(report.Checks, newDoctorCheck("tmux.ownership."+repo.ID, doctorSkipped, "ownership check requires complete Git and tmux snapshots", ""))
				continue
			}
			plan := reconcile.Build(repo, snapshot, tmuxSnapshot)
			if len(plan.Conflicts) > 0 {
				report.Checks = append(report.Checks, newDoctorCheck("tmux.ownership."+repo.ID, doctorError, "tmux ownership conflicts require attention", "resolve conflicts before reconciling", plan.Conflicts...))
			} else {
				report.Checks = append(report.Checks, newDoctorCheck("tmux.ownership."+repo.ID, doctorOK, "no tmux ownership conflicts", ""))
			}
		}
	} else {
		for _, repo := range diagnosticRepos {
			report.Checks = append(report.Checks, newDoctorCheck("tmux.ownership."+repo.ID, doctorSkipped, "ownership check requires runtime-valid configuration", ""))
		}
	}

	report.Checks = append(report.Checks, s.doctorLaunchd(ctx, timeout, configHome, dataHome, stateHome)...)
	return report
}

func (s *Service) doctorLaunchd(ctx context.Context, timeout time.Duration, configHome, dataHome, stateHome string) []doctorCheck {
	if runtime.GOOS != "darwin" {
		return []doctorCheck{newDoctorCheck("launchd.support", doctorSkipped, "LaunchAgent management is supported only on macOS", "")}
	}
	home, err := userHome()
	if err != nil {
		return []doctorCheck{
			newDoctorCheck("launchd.plist", doctorError, "user home is unavailable", "repair HOME", err.Error()),
			newDoctorCheck("launchd.lifecycle", doctorSkipped, "lifecycle check requires an absolute user home", ""),
			newDoctorCheck("launchd.environment", doctorSkipped, "environment check requires an absolute user home", ""),
		}
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", launchd.Label+".plist")
	data, readErr := os.ReadFile(plist) //nolint:gosec // fixed per-user LaunchAgent path
	target := "gui/" + strconv.Itoa(os.Getuid()) + "/" + launchd.Label
	if errors.Is(readErr, os.ErrNotExist) {
		output, lifecycleErr := s.doctorRun(ctx, timeout, "launchctl", "print", target)
		switch {
		case lifecycleErr == nil:
			return []doctorCheck{
				newDoctorCheck("launchd.plist", doctorError, "owned plist is missing while its label is loaded", "run wts daemon uninstall, then reinstall"),
				newDoctorCheck("launchd.lifecycle", doctorError, "LaunchAgent label is loaded without the owned plist", "run wts daemon uninstall, then reinstall"),
				newDoctorCheck("launchd.environment", doctorSkipped, "environment check requires the owned plist", ""),
			}
		case launchd.IsNotFound(output, lifecycleErr):
			return []doctorCheck{
				newDoctorCheck("launchd.plist", doctorOK, "LaunchAgent is not installed", ""),
				newDoctorCheck("launchd.lifecycle", doctorOK, "LaunchAgent is not installed", ""),
				newDoctorCheck("launchd.environment", doctorSkipped, "environment check requires an installed LaunchAgent", ""),
			}
		default:
			return []doctorCheck{
				newDoctorCheck("launchd.plist", doctorOK, "LaunchAgent plist is not installed", ""),
				newDoctorCheck("launchd.lifecycle", doctorError, "LaunchAgent state is unavailable", "inspect launchctl before installing"),
				newDoctorCheck("launchd.environment", doctorSkipped, "environment check requires the owned plist", ""),
			}
		}
	}
	checks := make([]doctorCheck, 0, 3)
	if readErr != nil {
		checks = append(checks, newDoctorCheck("launchd.plist", doctorError, "LaunchAgent plist is unreadable", "rerun wts daemon install", readErr.Error()))
	} else {
		checks = append(checks, doctorFileCheck("launchd.plist", plist, false))
	}
	output, lifecycleErr := s.doctorRun(ctx, timeout, "launchctl", "print", target)
	switch {
	case lifecycleErr == nil:
		checks = append(checks, newDoctorCheck("launchd.lifecycle", doctorOK, "LaunchAgent is running", ""))
	case launchd.IsNotFound(output, lifecycleErr):
		checks = append(checks, newDoctorCheck("launchd.lifecycle", doctorWarning, "LaunchAgent is installed but stopped", "run wts daemon start"))
	default:
		checks = append(checks, newDoctorCheck("launchd.lifecycle", doctorError, "LaunchAgent state is unavailable", "run wts daemon status or inspect launchctl"))
	}
	if readErr != nil {
		checks = append(checks, newDoctorCheck("launchd.environment", doctorSkipped, "environment check requires a readable plist", ""))
		return checks
	}
	environment, parseErr := launchd.ParseEnvironment(data)
	if parseErr != nil {
		checks = append(checks, newDoctorCheck("launchd.environment", doctorError, "LaunchAgent environment is invalid", "rerun wts daemon install", parseErr.Error()))
		return checks
	}
	expected := map[string]string{"XDG_CONFIG_HOME": configHome, "XDG_DATA_HOME": dataHome, "XDG_STATE_HOME": stateHome}
	mismatches := make([]string, 0)
	for key, value := range expected {
		if environment[key] != value {
			mismatches = append(mismatches, fmt.Sprintf("%s=%q, expected %q", key, environment[key], value))
		}
	}
	if len(mismatches) > 0 {
		sort.Strings(mismatches)
		checks = append(checks, newDoctorCheck("launchd.environment", doctorError, "LaunchAgent XDG environment differs from this CLI", "rerun wts daemon install", mismatches...))
	} else {
		checks = append(checks, newDoctorCheck("launchd.environment", doctorOK, "LaunchAgent XDG environment matches this CLI", ""))
	}
	return checks
}

func (s *Service) doctor(ctx context.Context, jsonOutput bool) (string, error) {
	report := s.collectDoctor(ctx)
	var output string
	if jsonOutput {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return "", err
		}
		output = string(data)
	} else {
		output = renderDoctorHuman(report)
	}
	if doctorHasErrors(report) {
		return output, fmt.Errorf("doctor found errors")
	}
	return output, nil
}
