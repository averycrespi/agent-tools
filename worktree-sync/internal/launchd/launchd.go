package launchd

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
)

const Label = "dev.agent-tools.worktree-sync"
const daemonPath = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"

type Environment struct {
	ConfigHome string
	DataHome   string
	StateHome  string
}

type runner interface {
	Run(context.Context, string, string, ...string) ([]byte, error)
	Interactive(context.Context, string, string, ...string) error
}

type Manager struct {
	runner  runner
	goos    string
	home    string
	timeout time.Duration
	uid     int
}

func New(runner runner, goos, home string, timeout time.Duration) *Manager {
	return &Manager{runner: runner, goos: goos, home: home, timeout: timeout, uid: os.Getuid()}
}

func escaped(value string) string {
	var buffer bytes.Buffer
	_ = xml.EscapeText(&buffer, []byte(value))
	return buffer.String()
}

func Render(binary, home string, environment Environment) ([]byte, error) {
	if !filepath.IsAbs(binary) {
		return nil, fmt.Errorf("wtsd path must be absolute")
	}
	for name, value := range map[string]string{
		"XDG_CONFIG_HOME": environment.ConfigHome,
		"XDG_DATA_HOME":   environment.DataHome,
		"XDG_STATE_HOME":  environment.StateHome,
	} {
		if !filepath.IsAbs(value) {
			return nil, fmt.Errorf("%s must be absolute", name)
		}
	}
	stdout := filepath.Join(home, "Library", "Logs", "worktree-sync.log")
	stderr := filepath.Join(home, "Library", "Logs", "worktree-sync.error.log")
	content := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + Label + `</string>
  <key>ProgramArguments</key><array><string>` + escaped(binary) + `</string></array>
  <key>EnvironmentVariables</key><dict>
    <key>PATH</key><string>` + daemonPath + `</string>
    <key>XDG_CONFIG_HOME</key><string>` + escaped(environment.ConfigHome) + `</string>
    <key>XDG_DATA_HOME</key><string>` + escaped(environment.DataHome) + `</string>
    <key>XDG_STATE_HOME</key><string>` + escaped(environment.StateHome) + `</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>` + escaped(stdout) + `</string>
  <key>StandardErrorPath</key><string>` + escaped(stderr) + `</string>
</dict></plist>
`
	return []byte(content), nil
}

func (m *Manager) supported() error {
	if m.goos != "darwin" {
		return fmt.Errorf("LaunchAgent management is supported only on macOS; run wtsd in the foreground")
	}
	return nil
}
func (m *Manager) domain() string { return "gui/" + strconv.Itoa(m.uid) }
func (m *Manager) target() string { return m.domain() + "/" + Label }
func (m *Manager) plist() string {
	return filepath.Join(m.home, "Library", "LaunchAgents", Label+".plist")
}
func (m *Manager) lifecycleLock() string {
	return filepath.Join(m.home, "Library", "Application Support", "worktree-sync", "launchd.lock")
}
func (m *Manager) logPaths() []string {
	return []string{
		filepath.Join(m.home, "Library", "Logs", "worktree-sync.log"),
		filepath.Join(m.home, "Library", "Logs", "worktree-sync.error.log"),
	}
}
func (m *Manager) runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.timeout)
		defer cancel()
	}
	return m.runner.Run(ctx, "", name, args...)
}
func (m *Manager) run(ctx context.Context, args ...string) ([]byte, error) {
	return m.runCommand(ctx, "launchctl", args...)
}

func (m *Manager) withLifecycleLock(ctx context.Context, operation func() (string, error)) (string, error) {
	if err := m.supported(); err != nil {
		return "", err
	}
	dir := filepath.Dir(m.lifecycleLock())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating LaunchAgent lock directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // fixed per-user lifecycle lock must remain private
		return "", fmt.Errorf("securing LaunchAgent lock directory: %w", err)
	}
	lock, err := state.Acquire(ctx, m.lifecycleLock())
	if err != nil {
		return "", fmt.Errorf("acquiring LaunchAgent lifecycle lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()
	return operation()
}

type lifecycleState int

const (
	stateNotInstalled lifecycleState = iota
	stateStopped
	stateRunning
)

type observedState struct {
	state lifecycleState
}

func IsNotFound(output []byte, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(string(output))
	return strings.Contains(text, "could not find service") || strings.Contains(text, "service not found")
}

func launchctlNotFound(output []byte, err error) bool { return IsNotFound(output, err) }

func ParseEnvironment(data []byte) (map[string]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	environment := make(map[string]string)
	awaitingDictionary := false
	inDictionary := false
	currentKey := ""
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decoding LaunchAgent plist: %w", err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "key":
				var value string
				if err := decoder.DecodeElement(&value, &element); err != nil {
					return nil, fmt.Errorf("decoding LaunchAgent plist key: %w", err)
				}
				if !inDictionary && value == "EnvironmentVariables" {
					awaitingDictionary = true
				} else if inDictionary {
					currentKey = value
				}
			case "dict":
				if awaitingDictionary {
					awaitingDictionary = false
					inDictionary = true
				}
			case "string":
				if inDictionary && currentKey != "" {
					var value string
					if err := decoder.DecodeElement(&value, &element); err != nil {
						return nil, fmt.Errorf("decoding LaunchAgent environment value: %w", err)
					}
					environment[currentKey] = value
					currentKey = ""
				}
			}
		case xml.EndElement:
			if inDictionary && element.Name.Local == "dict" {
				return environment, nil
			}
		}
	}
	return nil, fmt.Errorf("LaunchAgent plist has no EnvironmentVariables dictionary")
}

func (m *Manager) observe(ctx context.Context) (observedState, error) {
	_, statErr := os.Stat(m.plist())
	installed := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return observedState{}, fmt.Errorf("checking LaunchAgent plist: %w", statErr)
	}
	output, err := m.run(ctx, "print", m.target())
	if err == nil {
		if !installed {
			return observedState{}, fmt.Errorf("observing LaunchAgent: label is loaded but owned plist is missing")
		}
		return observedState{state: stateRunning}, nil
	}
	if !launchctlNotFound(output, err) {
		return observedState{}, fmt.Errorf("observing LaunchAgent: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if installed {
		return observedState{state: stateStopped}, nil
	}
	return observedState{state: stateNotInstalled}, nil
}

func (m *Manager) bootstrap(ctx context.Context) error {
	output, err := m.run(ctx, "bootstrap", m.domain(), m.plist())
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *Manager) bootout(ctx context.Context) error {
	output, err := m.run(ctx, "bootout", m.target())
	if err != nil && !launchctlNotFound(output, err) {
		return fmt.Errorf("launchctl bootout: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *Manager) confirm(ctx context.Context, expected lifecycleState) error {
	observed, err := m.observe(ctx)
	if err != nil {
		return err
	}
	if observed.state != expected {
		return fmt.Errorf("LaunchAgent state did not reach the expected result")
	}
	return nil
}

type priorInstall struct {
	exists bool
	loaded bool
	data   []byte
	mode   os.FileMode
}

func (m *Manager) prior(observed observedState) (priorInstall, error) {
	if observed.state == stateNotInstalled {
		return priorInstall{}, nil
	}
	data, err := os.ReadFile(m.plist()) //nolint:gosec // fixed owned LaunchAgent path
	if err != nil {
		return priorInstall{}, fmt.Errorf("reading previous LaunchAgent plist: %w", err)
	}
	info, err := os.Stat(m.plist())
	if err != nil {
		return priorInstall{}, fmt.Errorf("stating previous LaunchAgent plist: %w", err)
	}
	return priorInstall{exists: true, loaded: observed.state == stateRunning, data: data, mode: info.Mode().Perm()}, nil
}

func (m *Manager) restore(ctx context.Context, prior priorInstall) error {
	var restoreErrs []error
	if err := m.bootout(ctx); err != nil {
		restoreErrs = append(restoreErrs, err)
	}
	if prior.exists {
		if err := state.AtomicWrite(m.plist(), prior.data, prior.mode); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("restoring previous LaunchAgent plist: %w", err))
		} else if prior.loaded {
			if err := m.bootstrap(ctx); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restoring previous loaded LaunchAgent: %w", err))
			}
		}
	} else if err := os.Remove(m.plist()); err != nil && !os.IsNotExist(err) {
		restoreErrs = append(restoreErrs, fmt.Errorf("removing failed LaunchAgent plist: %w", err))
	}
	return errors.Join(restoreErrs...)
}

func (m *Manager) Install(ctx context.Context, binary string, environment Environment) (string, error) {
	data, err := Render(binary, m.home, environment)
	if err != nil {
		return "", err
	}
	return m.withLifecycleLock(ctx, func() (string, error) {
		observed, observeErr := m.observe(ctx)
		if observeErr != nil {
			return "", observeErr
		}
		previous, previousErr := m.prior(observed)
		if previousErr != nil {
			return "", previousErr
		}
		if err := os.MkdirAll(filepath.Join(m.home, "Library", "Logs"), 0o700); err != nil {
			return "", err
		}
		if err := state.AtomicWrite(m.plist(), data, 0o600); err != nil {
			return "", err
		}
		if observed.state == stateRunning {
			if err := m.bootout(ctx); err != nil {
				if restoreErr := state.AtomicWrite(m.plist(), previous.data, previous.mode); restoreErr != nil {
					return "LaunchAgent update failed; rollback incomplete; rerun wts daemon install after checking the plist", errors.Join(err, restoreErr)
				}
				return "LaunchAgent update failed; previous plist restored", err
			}
		}
		if err := m.bootstrap(ctx); err != nil {
			restoreErr := m.restore(ctx, previous)
			if restoreErr == nil {
				if previous.exists {
					return "LaunchAgent update failed; previous LaunchAgent restored", err
				}
				return "LaunchAgent installation failed; no LaunchAgent installed", err
			}
			return "LaunchAgent installation failed; rollback incomplete", errors.Join(err, restoreErr)
		}
		if err := m.confirm(ctx, stateRunning); err != nil {
			return "LaunchAgent files updated but running state could not be confirmed", err
		}
		return "LaunchAgent installed and running", nil
	})
}

func (m *Manager) Uninstall(ctx context.Context) (string, error) {
	return m.withLifecycleLock(ctx, func() (string, error) {
		observed, err := m.observe(ctx)
		if err != nil {
			return "", err
		}
		if observed.state == stateNotInstalled {
			return "LaunchAgent is not installed", nil
		}
		if observed.state == stateRunning {
			if err := m.bootout(ctx); err != nil {
				return "", err
			}
		}
		if err := os.Remove(m.plist()); err != nil && !os.IsNotExist(err) {
			return "LaunchAgent stopped but plist removal failed; retry wts daemon uninstall", fmt.Errorf("removing LaunchAgent: %w", err)
		}
		return "LaunchAgent uninstalled", nil
	})
}

func (m *Manager) Start(ctx context.Context) (string, error) {
	return m.withLifecycleLock(ctx, func() (string, error) {
		observed, err := m.observe(ctx)
		if err != nil {
			return "", err
		}
		switch observed.state {
		case stateRunning:
			return "LaunchAgent is already running", nil
		case stateNotInstalled:
			return "", fmt.Errorf("LaunchAgent is not installed; run wts daemon install")
		}
		if err := m.bootstrap(ctx); err != nil {
			return "", err
		}
		if err := m.confirm(ctx, stateRunning); err != nil {
			return "LaunchAgent start requested but running state could not be confirmed", err
		}
		return "LaunchAgent started", nil
	})
}

func (m *Manager) Stop(ctx context.Context) (string, error) {
	return m.withLifecycleLock(ctx, func() (string, error) {
		observed, err := m.observe(ctx)
		if err != nil {
			return "", err
		}
		switch observed.state {
		case stateNotInstalled:
			return "LaunchAgent is not installed; nothing to stop", nil
		case stateStopped:
			return "LaunchAgent is already stopped", nil
		}
		if err := m.bootout(ctx); err != nil {
			return "", err
		}
		if err := m.confirm(ctx, stateStopped); err != nil {
			return "LaunchAgent stop requested but stopped state could not be confirmed", err
		}
		return "LaunchAgent stopped", nil
	})
}

func (m *Manager) Restart(ctx context.Context) (string, error) {
	return m.withLifecycleLock(ctx, func() (string, error) {
		observed, err := m.observe(ctx)
		if err != nil {
			return "", err
		}
		if observed.state == stateNotInstalled {
			return "", fmt.Errorf("LaunchAgent is not installed; run wts daemon install")
		}
		if observed.state == stateRunning {
			if err := m.bootout(ctx); err != nil {
				return "", err
			}
		}
		if err := m.bootstrap(ctx); err != nil {
			return "LaunchAgent stopped but restart failed; run wts daemon start", err
		}
		if err := m.confirm(ctx, stateRunning); err != nil {
			return "LaunchAgent restart requested but running state could not be confirmed", err
		}
		return "LaunchAgent restarted", nil
	})
}

func (m *Manager) Status(ctx context.Context) (string, error) {
	return m.withLifecycleLock(ctx, func() (string, error) {
		observed, err := m.observe(ctx)
		if err != nil {
			return "", err
		}
		switch observed.state {
		case stateRunning:
			return "LaunchAgent running", nil
		case stateStopped:
			return "LaunchAgent stopped (installed)", nil
		default:
			return "LaunchAgent not installed", nil
		}
	})
}

func (m *Manager) Logs(ctx context.Context, lines int, follow bool) (string, error) {
	if err := m.supported(); err != nil {
		return "", err
	}
	if lines < 1 {
		return "", fmt.Errorf("log line count must be positive")
	}
	paths := m.logPaths()
	if follow {
		existing := make([]string, 0, len(paths))
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				existing = append(existing, path)
			} else if !os.IsNotExist(err) {
				return "", fmt.Errorf("checking daemon log %s: %w", filepath.Base(path), err)
			}
		}
		if len(existing) == 0 {
			return "", fmt.Errorf("LaunchAgent logs have not been created; install or start the daemon first")
		}
		args := append([]string{"-n", strconv.Itoa(lines), "-F"}, existing...)
		err := m.runner.Interactive(ctx, "", "/usr/bin/tail", args...)
		if ctx.Err() != nil {
			return "", nil
		}
		return "", err
	}
	var output strings.Builder
	for i, path := range paths {
		if i > 0 {
			output.WriteByte('\n')
		}
		fmt.Fprintf(&output, "==> %s <==\n", path)
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				output.WriteString("log has not been created\n")
				continue
			}
			return "", fmt.Errorf("checking daemon log %s: %w", filepath.Base(path), err)
		}
		content, err := m.runCommand(ctx, "/usr/bin/tail", "-n", strconv.Itoa(lines), path)
		if err != nil {
			return "", fmt.Errorf("reading daemon log %s: %w", filepath.Base(path), err)
		}
		output.Write(content)
		if len(content) > 0 && content[len(content)-1] != '\n' {
			output.WriteByte('\n')
		}
	}
	return strings.TrimSuffix(output.String(), "\n"), nil
}
