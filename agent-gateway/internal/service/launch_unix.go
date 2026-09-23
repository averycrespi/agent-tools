//go:build darwin || linux

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type job struct {
	Loaded bool
	PID    string
	State  string
}
type processIdentity struct {
	PID        string
	Start      string
	Executable string
	State      string
}

func (m *manager) target() string { return "gui/" + strconv.Itoa(m.uid) + "/" + Label }
func (m *manager) observe(ctx context.Context, d definition) (job, error) {
	data, code, err := m.run(ctx, "/bin/launchctl", "print", m.target())
	if err != nil {
		return job{}, fmt.Errorf("launchd inspection unknown; no mutation is safe: %w", err)
	}
	if code != 0 {
		expected := "Could not find service \"" + Label + "\" in domain"
		if strings.Contains(string(data), expected) {
			return job{State: "unloaded"}, nil
		}
		return job{}, errors.New("launchd inspection unsupported or unknown; check the logged-in GUI domain")
	}
	if d.Binary == "" {
		return job{Loaded: true}, nil
	}
	lines, err := launchServiceLines(string(data), m.target())
	if err != nil {
		return job{}, err
	}
	var path, program, state, pid []string
	var args []string
	inArgs := false
	foundArgs := false
	for _, line := range lines {
		if inArgs {
			if strings.TrimSpace(line) == "}" {
				inArgs = false
			} else {
				args = append(args, strings.TrimLeft(line, "\t "))
			}
			continue
		}
		line = strings.TrimLeft(line, "\t ")
		if line == "arguments = {" {
			if foundArgs {
				return job{}, errors.New("ambiguous loaded arguments")
			}
			foundArgs = true
			inArgs = true
			continue
		}
		for _, field := range []struct {
			prefix string
			values *[]string
		}{{"path = ", &path}, {"program = ", &program}, {"state = ", &state}, {"pid = ", &pid}} {
			if strings.HasPrefix(line, field.prefix) {
				*field.values = append(*field.values, strings.TrimPrefix(line, field.prefix))
			}
		}
	}
	expected := d.argv
	if expected == nil {
		expected = d.arguments()
	}
	if !slices.Equal(path, []string{m.plist()}) || !slices.Equal(program, []string{d.Binary}) || inArgs || !foundArgs || !slices.Equal(args, expected) || len(pid) > 1 || len(state) != 1 {
		return job{}, errors.New("loaded definition differs or is ambiguous; reconcile launchd and installed selections explicitly")
	}
	result := job{Loaded: true, State: "loaded-exited"}
	if len(pid) == 1 {
		n, e := strconv.Atoi(pid[0])
		if e != nil || n <= 0 {
			return job{}, errors.New("unknown loaded process identity")
		}
		result.PID = pid[0]
		result.State = "running"
	} else if !slices.Contains([]string{"not running", "waiting", "spawn scheduled", "exited"}, state[0]) {
		return job{}, errors.New("loaded state has no provable process identity")
	}
	return result, nil
}

// launchServiceLines projects only direct service fields and its literal argv.
// launchctl print is not a stable serialization format: uncertain structure must
// refuse identity rather than promote fields from coalitions or other subobjects.
func launchServiceLines(data, target string) ([]string, error) {
	type block struct {
		indent string
		child  string
		args   bool
	}
	var stack []block
	var result []string
	started := false
	invalid := errors.New("ambiguous loaded service structure")
	for _, raw := range strings.Split(data, "\n") {
		line := strings.TrimLeft(raw, "\t ")
		indent := raw[:len(raw)-len(line)]
		if line == "" {
			if len(stack) > 0 && stack[len(stack)-1].args {
				result = append(result, "")
			}
			continue
		}
		if !started {
			if raw != target+" = {" {
				return nil, invalid
			}
			started = true
			stack = append(stack, block{})
			continue
		}
		if len(stack) == 0 {
			return nil, invalid
		}
		current := &stack[len(stack)-1]
		if line == "}" && indent == current.indent {
			if current.args {
				result = append(result, "}")
			}
			stack = stack[:len(stack)-1]
			continue
		}
		if !strings.HasPrefix(indent, current.indent) || len(indent) <= len(current.indent) {
			return nil, invalid
		}
		// Irrelevant sections have their own launchctl-specific formats (such
		// as LWCR dictionaries and arrays). Only their enclosing boundary is
		// identity-relevant: never parse or promote their deeper contents.
		if len(stack) > 1 && !current.args {
			continue
		}
		if current.child == "" {
			current.child = indent
		} else if indent != current.child {
			return nil, invalid
		}
		if current.args {
			result = append(result, line)
			continue
		}
		if line == "}" {
			return nil, invalid
		}
		top := len(stack) == 1
		if top {
			result = append(result, line)
		}
		if strings.HasSuffix(line, " = {") {
			stack = append(stack, block{indent: indent, args: top && line == "arguments = {"})
		}
	}
	if !started || len(stack) != 0 {
		return nil, invalid
	}
	return result, nil
}

func (m *manager) process(ctx context.Context, pid, binary string) (processIdentity, error) {
	p, err := m.inspectProcess(ctx, pid)
	if err != nil || p.PID == "" {
		return p, err
	}
	if p.State[0] == 'Z' || p.Executable != binary {
		return processIdentity{}, errors.New("process executable identity changed")
	}
	return p, nil
}

func (m *manager) inspectProcess(ctx context.Context, pid string) (processIdentity, error) {
	data, code, err := m.run(ctx, "/bin/ps", "-ww", "-p", pid, "-o", "uid=,lstart=,state=,comm=")
	if err != nil {
		return processIdentity{}, errors.New("process inspection unknown")
	}
	if code == 1 && len(strings.TrimSpace(string(data))) == 0 {
		return processIdentity{}, nil
	}
	fields := strings.Fields(string(data))
	line := strings.TrimRight(string(data), "\r\n")
	if code != 0 || len(fields) < 7 || fields[0] != strconv.Itoa(m.uid) || strings.ContainsAny(line, "\r\n") {
		return processIdentity{}, errors.New("process identity unavailable")
	}
	start := strings.Join(fields[1:6], " ")
	if _, err := time.Parse("Mon Jan 2 15:04:05 2006", start); err != nil {
		return processIdentity{}, errors.New("process start time unavailable")
	}
	state := fields[6]
	if !strings.ContainsAny(state[:1], "RSITUZD") || strings.Trim(state[1:], "<NXEVLs+>Wl") != "" {
		return processIdentity{}, errors.New("process state unavailable")
	}
	// Consume columns, not a year substring that could also occur in the UID.
	// Preserve literal internal and trailing executable whitespace.
	for range 7 {
		line = strings.TrimLeft(line, "\t ")
		end := strings.IndexAny(line, "\t ")
		if end < 0 {
			line = ""
			break
		}
		line = line[end:]
	}
	return processIdentity{PID: pid, Start: start, State: state, Executable: strings.TrimLeft(line, "\t ")}, nil
}

// Scoped inspection never scans arbitrary process arguments or native credentials.
// Exact selected executables are candidates, not basename-wide ownership claims.
func (m *manager) residual(ctx context.Context, d definition) ([]processIdentity, error) {
	return m.residualExcept(ctx, d, nil)
}

// Already observed tracked processes remain blockers, not new ownership candidates.
func (m *manager) residualExcept(ctx context.Context, d definition, remaining []processIdentity) ([]processIdentity, error) {
	data, code, err := m.run(ctx, "/bin/ps", "-axwwo", "uid=,pid=,comm=")
	if err != nil || code != 0 || strings.TrimSpace(string(data)) == "" {
		return nil, errors.New("process inventory unknown")
	}
	var result []processIdentity
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 3 {
			return nil, errors.New("process inventory incomplete")
		}
		uid, e := strconv.Atoi(fields[0])
		pid, e2 := strconv.Atoi(fields[1])
		if e != nil || e2 != nil || pid <= 0 {
			return nil, errors.New("invalid process inventory")
		}
		if uid != m.uid || pid == os.Getpid() || slices.ContainsFunc(remaining, func(p processIdentity) bool { return p.PID == fields[1] }) {
			continue
		}
		rest := strings.TrimLeft(strings.TrimPrefix(strings.TrimLeft(line, "\t "), fields[0]), "\t ")
		binary := strings.TrimLeft(strings.TrimPrefix(rest, fields[1]), "\t ")
		if binary != d.Binary {
			continue
		}
		args, status, e := m.run(ctx, "/bin/ps", "-ww", "-p", fields[1], "-o", "command=")
		if e != nil {
			return nil, errors.New("candidate process arguments unknown")
		}
		if status == 1 && strings.TrimSpace(string(args)) == "" {
			continue
		}
		if status != 0 {
			return nil, errors.New("candidate process inspection failed")
		}
		relevant, e := relevantCommand(strings.TrimLeft(strings.TrimRight(string(args), "\r\n"), "\t "), d)
		if e != nil {
			return nil, e
		}
		if !relevant {
			continue
		}
		identity, e := m.process(ctx, fields[1], d.Binary)
		if e != nil {
			return nil, e
		}
		if identity.PID != "" {
			result = append(result, identity)
		}
	}
	return result, nil
}
func (m *manager) stopped(ctx context.Context, d definition, initial job) error {
	tracked, err := m.residual(ctx, d)
	if err != nil {
		return err
	}
	if initial.PID != "" {
		p, e := m.process(ctx, initial.PID, d.Binary)
		if e != nil {
			return e
		}
		if p.PID != "" {
			tracked = append(tracked, p)
		}
	}
	if !initial.Loaded {
		if len(tracked) != 0 {
			return errors.New("canonical job is unloaded but an installation process remains; no new owner is permitted")
		}
		return existingLockFree(d.DataDir, m.uid)
	}
	if _, err = m.mutation(ctx, "bootout", m.target()); err != nil {
		return fmt.Errorf("stop outcome unknown: %w; inspect status before another operation", err)
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for attempt := 0; attempt < 61; attempt++ {
		current, e := m.observe(bounded, d)
		if e != nil {
			return e
		}
		var remaining []processIdentity
		for _, old := range tracked {
			p, e := m.inspectProcess(bounded, old.PID)
			if e != nil {
				return e
			}
			if p.PID != "" {
				// A same-UID/start zombie permits waiting only. It is never
				// absence or authority to adopt a new installation owner.
				unavailableZombie := p.State[0] == 'Z' && (p.Executable == "" || p.Executable == "<defunct>" || (strings.HasPrefix(p.Executable, "(") && strings.HasSuffix(p.Executable, ")")))
				if p.Start != old.Start || (p.Executable != old.Executable && !unavailableZombie) {
					return errors.New("process identity changed after stop; no replacement permitted")
				}
				remaining = append(remaining, p)
			}
		}
		residual, e := m.residualExcept(bounded, d, remaining)
		if e != nil {
			return e
		}
		if !current.Loaded && len(remaining) == 0 && len(residual) == 0 {
			return existingLockFree(d.DataDir, m.uid)
		}
		select {
		case <-bounded.Done():
			return errors.New("stop unconfirmed within 30 seconds; no new owner permitted")
		case <-time.After(500 * time.Millisecond):
		}
	}
	return errors.New("stop observation bound exhausted")
}
func (m *manager) mutation(ctx context.Context, verb string, args ...string) (int, error) {
	_, code, err := m.run(ctx, "/bin/launchctl", append([]string{verb}, args...)...)
	if err != nil || code != 0 {
		return code, errors.New("launchctl " + verb + " failed or is uncertain; not retried")
	}
	return code, nil
}

func existingLockFree(root string, uid int) error {
	release, err := acquireExistingFence(root, uid)
	if release != nil {
		release()
	}
	return err
}

func acquireExistingFence(root string, uid int) (func(), error) {
	path := filepath.Join(root, "gateway.lock")
	if err := noSymlinks(path); err != nil {
		return nil, err
	}
	if err := inspect(path, uid, false, true); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, statErr := file.Stat()
	current, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || !os.SameFile(info, current) {
		_ = file.Close()
		return nil, errors.New("installation lock identity changed")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("selected installation still has an owner; no replacement permitted")
	}
	return func() { _ = file.Close() }, nil
}
