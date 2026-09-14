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
	PID      string
	Evidence string
}

func (m *manager) target() string { return "gui/" + strconv.Itoa(m.uid) + "/" + Label }
func (m *manager) observe(ctx context.Context, d definition) (job, error) {
	data, code, err := m.run(ctx, "/bin/launchctl", "print", m.target())
	if err != nil {
		return job{}, errors.New("launchd inspection unknown; no mutation is safe")
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
	var path, program, state, pid []string
	var args []string
	inArgs := false
	foundArgs := false
	for _, line := range strings.Split(string(data), "\n") {
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
func (m *manager) process(ctx context.Context, pid, binary string) (processIdentity, error) {
	data, code, err := m.run(ctx, "/bin/ps", "-ww", "-p", pid, "-o", "uid=,lstart=,comm=")
	if err != nil {
		return processIdentity{}, errors.New("process inspection unknown")
	}
	if code == 1 && len(strings.TrimSpace(string(data))) == 0 {
		return processIdentity{}, nil
	}
	fields := strings.Fields(string(data))
	if code != 0 || len(fields) < 7 || fields[0] != strconv.Itoa(m.uid) {
		return processIdentity{}, errors.New("process identity unavailable")
	}
	prefix := strings.Join(fields[1:6], " ")
	line := strings.TrimLeft(strings.TrimRight(string(data), "\r\n"), "\t ")
	start := strings.Index(line, fields[5])
	if start < 0 || strings.TrimLeft(line[start+len(fields[5]):], "\t ") != binary {
		return processIdentity{}, errors.New("process executable identity changed")
	}
	return processIdentity{PID: pid, Evidence: prefix + "\x00" + binary}, nil
}

// Scoped inspection never scans arbitrary process arguments or native credentials.
// Exact selected executables are candidates, not basename-wide ownership claims.
func (m *manager) residual(ctx context.Context, d definition) ([]processIdentity, error) {
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
		if uid != m.uid || pid == os.Getpid() {
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
		remaining, e := m.residual(bounded, d)
		if e != nil {
			return e
		}
		for _, old := range tracked {
			p, e := m.process(bounded, old.PID, d.Binary)
			if e != nil {
				return e
			}
			if p.PID != "" {
				if p != old {
					return errors.New("process identity changed after stop; no replacement permitted")
				}
				remaining = append(remaining, p)
			}
		}
		if !current.Loaded && len(remaining) == 0 {
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
