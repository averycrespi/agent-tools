//go:build darwin || linux

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
)

// Changes uses nil to preserve an installed selection; an empty host slice clears it.
type Changes struct {
	HTTPProxyListen    *string
	TrafficBudgetBytes *int64
	Binary             *string
	DataDir            *string
	Listen             *string
	AllowedHosts       *[]string
	LogLevel           *string
}

type Result struct {
	Installed bool      `json:"installed"`
	Settings  *Settings `json:"settings,omitempty"`
	Plist     string    `json:"plist"`
	Stdout    string    `json:"stdout,omitempty"`
	Stderr    string    `json:"stderr,omitempty"`
	Launchd   string    `json:"launchd"`
	Readiness string    `json:"readiness"`
	Message   string    `json:"message"`
	Warnings  []string  `json:"warnings,omitempty"`
}

type manager struct {
	home       string
	uid        int
	xdg        string
	executable string
	run        commandRunner
	publish    func(string, []byte, bool) (bool, error)
	probe      func(context.Context, string) string
}

// Execute rejects unsupported accounts/platforms before filesystem or subprocess work.
func Execute(ctx context.Context, operation string, changes Changes) (Result, error) {
	if runtime.GOOS != "darwin" {
		return Result{}, errors.New("service management requires macOS launchd")
	}
	if os.Getuid() == 0 || os.Geteuid() != os.Getuid() {
		return Result{}, errors.New("run as the intended logged-in user, without sudo")
	}
	account, err := user.Current()
	if err != nil || !absolute(account.HomeDir) {
		return Result{}, errors.New("OS-account home is unavailable")
	}
	executable, err := os.Executable()
	if err != nil {
		return Result{}, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return Result{}, err
	}
	m := manager{home: account.HomeDir, uid: os.Getuid(), xdg: os.Getenv("XDG_DATA_HOME"), executable: executable, run: runCommand, publish: publish, probe: probeReadiness}
	return m.execute(ctx, operation, changes)
}
func apply(s Settings, c Changes) Settings {
	if c.HTTPProxyListen != nil {
		s.HTTPProxyListen = *c.HTTPProxyListen
	}
	if c.TrafficBudgetBytes != nil {
		s.TrafficBudgetBytes = *c.TrafficBudgetBytes
	}
	if c.Binary != nil {
		s.Binary = *c.Binary
	}
	if c.DataDir != nil {
		s.DataDir = *c.DataDir
	}
	if c.Listen != nil {
		s.Listen = *c.Listen
	}
	if c.LogLevel != nil {
		s.LogLevel = *c.LogLevel
	}
	if c.AllowedHosts != nil {
		s.AllowedHosts = append([]string(nil), (*c.AllowedHosts)...)
	}
	return s
}
func (m *manager) execute(ctx context.Context, operation string, changes Changes) (result Result, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result = Result{Plist: m.plist(), Launchd: "unknown", Readiness: "not-probed"}
	if !absolute(m.home) {
		return result, errors.New("OS-account home must be absolute")
	}
	if err := inspect(m.home, m.uid, true, false); err != nil {
		return result, err
	}
	if operation == "install" {
		return m.install(ctx, changes, result)
	}
	d, data, info, err := m.read()
	if errors.Is(err, os.ErrNotExist) && operation == "status" {
		state, e := m.observe(ctx, definition{})
		if e != nil {
			return result, e
		}
		result.Launchd = state.State
		if state.Loaded {
			result.Launchd = "loaded-without-installed-definition"
		}
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if operation == "status" && legacyPlistEncoding(data) {
		result.Warnings = []string{"Installed plist uses legacy XML encoding that launchd may reject even when plutil -lint passes. No repair was performed. If unloaded and launchd reports error 109, back up the plist outside LaunchAgents, then normalize it with plutil -convert xml1 using the displayed plist path before starting. An unchanged service update does not rewrite it."}
	}
	result.Installed = true
	result.Settings = &d.Settings
	result.Stdout = d.Stdout
	result.Stderr = d.Stderr
	proposed := d
	if operation == "update" {
		proposed.Settings = apply(d.Settings, changes)
		if !reflect.DeepEqual(proposed.Settings, d.Settings) {
			proposed.argv = nil
		}
	}
	if operation == "start" || operation == "restart" || operation == "update" {
		if err = m.preflight(proposed, true); err != nil {
			return result, err
		}
	}
	encoded, err := proposed.encode()
	if err != nil {
		return result, err
	}
	if operation != "status" {
		release, e := m.lock()
		if e != nil {
			return result, e
		}
		defer release()
		if err = unchanged(m.plist(), m.uid, data, info); err != nil {
			return result, err
		}
	}
	state, err := m.observe(ctx, d)
	if err != nil {
		if operation == "status" {
			result.Readiness = m.probe(ctx, d.Listen)
			result.Message = err.Error() + "; installed selections are shown independently."
			return result, nil
		}
		return result, err
	}
	result.Launchd = state.State
	if operation == "status" {
		result.Readiness = m.probe(ctx, d.Listen)
		result.Message = "Readiness is a listener observation, not process identity or upstream credential health."
		return result, nil
	}
	if operation == "start" && state.Loaded {
		result.Message = "Already loaded; not restarted."
		return result, nil
	}
	if operation == "update" && reflect.DeepEqual(proposed.Settings, d.Settings) {
		result.Message = "Installed selections unchanged; not restarted."
		return result, nil
	}
	switch operation {
	case "start", "stop", "restart", "update", "uninstall":
	default:
		return result, errors.New("unknown service operation")
	}
	if operation == "update" && (proposed.DataDir != d.DataDir || proposed.Binary != d.Binary) {
		owners, e := m.residual(ctx, proposed)
		if e != nil {
			return result, e
		}
		if len(owners) != 0 {
			return result, errors.New("proposed installation already has a process owner")
		}
		if e = existingLockFree(proposed.DataDir, m.uid); e != nil {
			return result, e
		}
	}
	if err = m.stopped(ctx, d, state); err != nil {
		result.Launchd = "unknown"
		return result, err
	}
	result.Launchd = "unloaded"
	var fences []func()
	releaseFences := func() {
		for _, release := range fences {
			release()
		}
		fences = nil
	}
	defer releaseFences()
	roots := []string{d.DataDir}
	if proposed.DataDir != d.DataDir {
		roots = append(roots, proposed.DataDir)
	}
	for _, root := range roots {
		release, e := acquireExistingFence(root, m.uid)
		if e != nil {
			return result, e
		}
		if release != nil {
			fences = append(fences, release)
		}
	}
	if err = unchanged(m.plist(), m.uid, data, info); err != nil {
		return result, err
	}
	if operation == "stop" {
		result.Message = "Stopped; installed selections retained."
		return result, nil
	}
	if operation == "uninstall" {
		if err = os.Remove(m.plist()); err != nil {
			return result, err
		}
		result.Installed = false
		result.Message = "Removed only the canonical plist; binaries, logs, data and credentials retained."
		return result, nil
	}
	if operation == "update" {
		if proposed.DataDir != d.DataDir || proposed.Binary != d.Binary {
			owners, e := m.residual(ctx, proposed)
			if e != nil {
				return result, e
			}
			if len(owners) != 0 {
				return result, errors.New("proposed installation already has a process owner")
			}
		}
		published, e := m.publish(m.plist(), encoded, true)
		if published {
			result.Settings = &proposed.Settings
			result.Message = "New installed settings retained."
		}
		if e != nil {
			return result, fmt.Errorf("service is stopped; plist publication confirmed=%t: %w", published, e)
		}
		d = proposed
		data = encoded
		if !state.Loaded {
			result.Message = "Updated installed selections; service remains unloaded."
			return result, nil
		}
	}
	current, e := m.observe(ctx, d)
	if e != nil || current.Loaded {
		return result, errors.New("service ownership changed before start; not bootstrapped")
	}
	owners, e := m.residual(ctx, d)
	if e != nil {
		return result, e
	}
	if len(owners) != 0 {
		return result, errors.New("installation acquired a process owner before start; not bootstrapped")
	}
	_, installed, _, e := m.read()
	if e != nil || !bytes.Equal(installed, data) {
		return result, errors.New("installed definition changed before start; not bootstrapped")
	}
	// Retain the installation fence through publication and all final reads.
	// Release only for the one launch handoff so serve can acquire its own lock.
	// The canonical management lock still excludes other service commands;
	// manual launchers must remain disabled throughout this serialized handoff.
	releaseFences()
	if _, err = m.mutation(ctx, "bootstrap", "gui/"+fmt.Sprint(m.uid), m.plist()); err != nil {
		result.Launchd = "unknown"
		return result, fmt.Errorf("installed settings retained; bootstrap outcome unknown: %w", err)
	}
	result.Launchd = "launch-accepted"
	result.Message = "Launch accepted; readiness and credential health have not been established."
	return result, nil
}
func (m *manager) install(ctx context.Context, changes Changes, result Result) (Result, error) {
	root := filepath.Join(m.home, ".local", "share", "agent-gateway")
	if changes.DataDir == nil && m.xdg != "" {
		if !absolute(m.xdg) {
			return result, errors.New("XDG_DATA_HOME must be an absolute path")
		}
		root = filepath.Join(m.xdg, "agent-gateway")
	}
	d := definition{Settings: apply(Settings{Binary: m.executable, DataDir: root, Listen: "127.0.0.1:8210", TrafficBudgetBytes: 4294967296}, changes), Stdout: filepath.Join(m.logs(), "stdout.log"), Stderr: filepath.Join(m.logs(), "stderr.log")}
	if _, err := os.Lstat(m.plist()); !errors.Is(err, os.ErrNotExist) {
		return result, errors.New("canonical plist already exists or is inaccessible; use service update")
	}
	if err := m.preflight(d, false); err != nil {
		return result, err
	}
	data, err := d.encode()
	if err != nil {
		return result, err
	}
	state, err := m.observe(ctx, definition{})
	if err != nil {
		return result, err
	}
	if state.Loaded {
		return result, errors.New("canonical service already loaded; install refused")
	}
	if err = m.prepareDirectories(); err != nil {
		return result, err
	}
	release, err := m.lock()
	if err != nil {
		return result, err
	}
	defer release()
	state, err = m.observe(ctx, definition{})
	if err != nil {
		return result, err
	}
	if state.Loaded {
		return result, errors.New("canonical service became loaded; install refused")
	}
	if _, err = os.Lstat(m.plist()); !errors.Is(err, os.ErrNotExist) {
		return result, errors.New("canonical plist appeared; install refused")
	}
	if err = m.preflight(d, false); err != nil {
		return result, err
	}
	if err = m.prepareLogs(d); err != nil {
		return result, err
	}
	published, err := m.publish(m.plist(), data, false)
	result.Installed = published
	result.Settings = &d.Settings
	result.Stdout = d.Stdout
	result.Stderr = d.Stderr
	result.Launchd = "unloaded"
	result.Message = "Installed without initializing storage or starting a service. Use service start explicitly."
	return result, err
}
func probeReadiness(ctx context.Context, listen string) string {
	client, err := controlclient.New("http://"+listen, controlclient.TransportOptions{ConnectTimeout: time.Second, HeaderTimeout: time.Second, RequestTimeout: 2 * time.Second})
	if err != nil {
		return "unknown"
	}
	response, err := client.Do(ctx, controlclient.Request{Method: http.MethodGet, Path: "/readyz"})
	if err != nil {
		return "unavailable"
	}
	if response.StatusCode == http.StatusOK && bytes.Equal(bytes.TrimSpace(response.Body), []byte(`{"status":"ready"}`)) {
		return "ready"
	}
	if response.StatusCode == http.StatusServiceUnavailable && bytes.Equal(bytes.TrimSpace(response.Body), []byte(`{"status":"not_ready"}`)) {
		return "not-ready"
	}
	return "unknown"
}
