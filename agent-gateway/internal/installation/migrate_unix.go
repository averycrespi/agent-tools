//go:build darwin || linux

// Package installation owns explicit stopped host-path migration, never storage recovery.
package installation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// Selection is supplied by the operator, not resolved from installation defaults.
type Selection struct {
	Source         string
	Destination    string
	InstallationID string
	ServiceBinary  string
	Confirm        bool
}

type Result struct {
	Source         string `json:"source"`
	Destination    string `json:"destination"`
	InstallationID string `json:"installation_id"`
	Migrated       bool   `json:"migrated"`
}

type hostInspector struct {
	platform string
	home     func() (string, error)
	run      func(context.Context, string, ...string) ([]byte, int, error)
}

// Migrate never stops/starts a service. It requires an already stopped installation
// and archived LaunchAgent plists, then rechecks those facts while holding the lock.
func Migrate(ctx context.Context, selection Selection) (Result, error) {
	return migrate(ctx, selection, hostInspector{platform: runtime.GOOS, home: accountHome, run: runInspection})
}

func migrate(ctx context.Context, selection Selection, host hostInspector) (result Result, resultErr error) {
	result = Result{Source: selection.Source, Destination: selection.Destination, InstallationID: selection.InstallationID}
	if selection.Source == "" || selection.Destination == "" || selection.InstallationID == "" || !filepath.IsAbs(selection.ServiceBinary) {
		return result, errors.New("migration requires explicit absolute source, destination, service binary and installation ID")
	}
	if err := host.stopped(ctx, selection.ServiceBinary); err != nil {
		return result, err
	}
	if paths.RelocationCompleted(selection.Source, selection.Destination) {
		return result, errors.New("migration already exchanged; do not replay; verify the destination and retain the source tombstone")
	}
	relocation, err := paths.InspectRelocation(selection.Source, selection.Destination)
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, relocation.Close()) }()
	identity, err := storage.InspectBaseIdentity(ctx, filepath.Join(selection.Source, paths.DatabaseName))
	if err != nil {
		return result, errors.New("stopped base identity is unreadable or unsupported; retain all state and use the migration runbook")
	}
	if identity.InstallationID != selection.InstallationID {
		return result, errors.New("source installation ID does not match explicit selection")
	}
	if !selection.Confirm {
		return result, nil
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = host.stopped(ctx, selection.ServiceBinary); err != nil {
		return result, err
	}
	if err = relocation.Commit(); err != nil {
		return result, err
	}
	result.Migrated = true
	return result, relocation.Close()
}

func accountHome() (string, error) {
	account, err := user.Current()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(account.HomeDir) {
		return "", errors.New("OS-account home is unavailable")
	}
	return account.HomeDir, nil
}

func (host hostInspector) stopped(ctx context.Context, binary string) error {
	if host.platform == "darwin" {
		home, err := host.home()
		if err != nil {
			return err
		}
		for _, label := range []string{"dev.agent-tools.mcp-gateway", "dev.agent-tools.agent-gateway"} {
			plist := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
			if _, err = os.Lstat(plist); !errors.Is(err, os.ErrNotExist) {
				return errors.New("archive both Gateway plists outside LaunchAgents after proven stop; migration does not manage services")
			}
			output, status, runErr := host.run(ctx, "/bin/launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+label)
			if runErr != nil {
				return errors.New("service inspection failed or timed out; migration refused")
			}
			diagnostic := "Could not find service \"" + label + "\" in domain"
			if status == 0 || !strings.Contains(string(output), diagnostic) {
				return errors.New("gateway service is loaded or ownership is unknown; migration refused")
			}
		}
	} else {
		return errors.New("automatic migration currently requires macOS launchd inspection; other supervisors require a separately qualified handover")
	}
	// Inspect the full process table, not a remembered PID. This never signals a
	// process and refuses every matching executable, including another migration.
	// Operators must disable other supervisors and serialize management; flock is
	// the final storage-owner gate, not a claim that an arbitrary PID exited.
	output, status, err := host.run(ctx, "/bin/ps", "-axwwo", "uid=,pid=,comm=")
	if err != nil || status != 0 {
		return errors.New("process absence could not be established")
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return errors.New("process inspection was empty")
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 3 {
			return errors.New("process inspection was incomplete")
		}
		uid, uidErr := strconv.Atoi(fields[0])
		pid, pidErr := strconv.Atoi(fields[1])
		if uidErr != nil || pidErr != nil || pid <= 0 {
			return errors.New("process inspection was invalid")
		}
		executable := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0])), fields[1]))
		name := filepath.Base(executable)
		if uid == os.Getuid() && pid != os.Getpid() && (name == "agent-gateway" || name == "mcp-gateway" || name == filepath.Base(binary)) {
			return errors.New("a Gateway executable is still present; establish process exit before migration")
		}
	}
	return nil
}

type boundedOutput struct{ bytes.Buffer }

func (output *boundedOutput) Write(data []byte) (int, error) {
	if output.Len()+len(data) > 1<<20 {
		return 0, errors.New("inspection output exceeds bound")
	}
	return output.Buffer.Write(data)
}

func runInspection(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var command *exec.Cmd
	switch name {
	case "/bin/launchctl":
		command = exec.CommandContext(bounded, "/bin/launchctl", args...) //nolint:gosec // Package-owned read-only print argv, fixed native executable, no shell or PATH lookup.
	case "/bin/ps":
		command = exec.CommandContext(bounded, "/bin/ps", args...) //nolint:gosec // Package-owned process inspection argv, fixed native executable, no shell or PATH lookup.
	default:
		return nil, -1, errors.New("unsupported inspection command")
	}
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	command.WaitDelay = time.Second
	var output boundedOutput
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	if bounded.Err() != nil {
		return nil, -1, bounded.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return output.Bytes(), exit.ExitCode(), nil
		}
		return nil, -1, fmt.Errorf("bounded inspection failed: %w", err)
	}
	return output.Bytes(), 0, nil
}
