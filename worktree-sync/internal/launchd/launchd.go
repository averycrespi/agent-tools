package launchd

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
)

const Label = "dev.agent-tools.worktree-sync"
const daemonPath = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"

type runner interface {
	Run(context.Context, string, string, ...string) ([]byte, error)
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

func Render(binary, home string) ([]byte, error) {
	if !filepath.IsAbs(binary) {
		return nil, fmt.Errorf("wtsd path must be absolute")
	}
	stdout := filepath.Join(home, "Library", "Logs", "worktree-sync.log")
	stderr := filepath.Join(home, "Library", "Logs", "worktree-sync.error.log")
	content := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + Label + `</string>
  <key>ProgramArguments</key><array><string>` + escaped(binary) + `</string></array>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>` + daemonPath + `</string></dict>
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
func (m *Manager) target() string { return "gui/" + strconv.Itoa(m.uid) + "/" + Label }
func (m *Manager) plist() string {
	return filepath.Join(m.home, "Library", "LaunchAgents", Label+".plist")
}
func (m *Manager) run(ctx context.Context, args ...string) ([]byte, error) {
	if m.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.timeout)
		defer cancel()
	}
	return m.runner.Run(ctx, "", "launchctl", args...)
}

func (m *Manager) Install(ctx context.Context, binary string) error {
	if err := m.supported(); err != nil {
		return err
	}
	data, err := Render(binary, m.home)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(m.home, "Library", "Logs"), 0o700); err != nil {
		return err
	}
	if err := state.AtomicWrite(m.plist(), data, 0o600); err != nil {
		return err
	}
	_, _ = m.run(ctx, "bootout", m.target())
	output, err := m.run(ctx, "bootstrap", "gui/"+strconv.Itoa(m.uid), m.plist())
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *Manager) Uninstall(ctx context.Context) error {
	if err := m.supported(); err != nil {
		return err
	}
	_, _ = m.run(ctx, "bootout", m.target())
	if err := os.Remove(m.plist()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing LaunchAgent: %w", err)
	}
	return nil
}
func (m *Manager) Start(ctx context.Context) error {
	if err := m.supported(); err != nil {
		return err
	}
	_, err := m.run(ctx, "kickstart", "-k", m.target())
	return err
}
func (m *Manager) Stop(ctx context.Context) error {
	if err := m.supported(); err != nil {
		return err
	}
	_, err := m.run(ctx, "kill", "SIGTERM", m.target())
	return err
}
func (m *Manager) Status(ctx context.Context) (string, error) {
	if err := m.supported(); err != nil {
		return "", err
	}
	output, err := m.run(ctx, "print", m.target())
	return string(output), err
}
