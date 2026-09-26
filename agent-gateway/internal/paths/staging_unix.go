//go:build darwin || linux

package paths

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

var stagingSpace = struct {
	sync.Mutex
	reserved map[string]int64
}{reserved: make(map[string]int64)}

// ReserveHeadroom owns a cooperative filesystem staging allowance through
// settlement. Installation locks exclude other Gateway processes for the same
// root; this ledger also accounts for distinct roots sharing a filesystem.
// It is not a physical allocation or protection from external disk consumers.
func ReserveHeadroom(root string, bytes int64) (func(), error) {
	if bytes <= 0 || bytes > 64<<30 {
		return nil, ErrUnsafePath
	}
	var identity syscall.Stat_t
	if err := syscall.Stat(root, &identity); err != nil {
		return nil, err
	}
	device := fmt.Sprint(identity.Dev)
	stagingSpace.Lock()
	defer stagingSpace.Unlock()
	var stat unix.Statfs_t
	if err := unix.Statfs(root, &stat); err != nil {
		return nil, err
	}
	reserved := stagingSpace.reserved[device]
	if reserved < 0 || reserved > (1<<62)-bytes {
		return nil, fmt.Errorf("invalid staging allowance")
	}
	required := bytes + reserved
	if required <= 0 || stat.Bsize <= 0 {
		return nil, fmt.Errorf("invalid filesystem capacity")
	}
	if uint64(required)/uint64(stat.Bsize)+1 > stat.Bavail {
		return nil, fmt.Errorf("insufficient space for bounded storage staging")
	}
	stagingSpace.reserved[device] += bytes
	var once sync.Once
	return func() {
		once.Do(func() {
			stagingSpace.Lock()
			defer stagingSpace.Unlock()
			stagingSpace.reserved[device] -= bytes
			if stagingSpace.reserved[device] == 0 {
				delete(stagingSpace.reserved, device)
			}
		})
	}, nil
}

// AcquireStoppedExisting never creates a directory, lock, or run marker. The
// existing installation lock serializes explicit stopped storage migration.
func AcquireStoppedExisting(root string) (*Ownership, error) {
	return acquireExistingOwnership(root, true)
}

func acquireExistingOwnership(root string, inspectTree bool) (*Ownership, error) {
	layout, err := Resolve(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(layout.Root)
	if err != nil {
		return nil, err
	}
	if err = validateOwnerOnlyDirectory(info, layout.Root); err != nil {
		return nil, err
	}
	rootInfo := info
	fd, err := unix.Open(layout.Lock, unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), layout.Lock)
	info, err = lock.Stat()
	if err == nil {
		err = validateOwnerOnlyFile(info, layout.Lock)
	}
	if err == nil {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	}
	if err != nil {
		_ = lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrInUse
		}
		return nil, err
	}
	if inspectTree {
		if err = inspectStagingTree(layout.Root, rootInfo); err != nil {
			return nil, errors.Join(err, releaseFileLock(lock))
		}
	}
	return &Ownership{layout: layout, lock: lock}, nil
}

// ProbeOwnership observes the existing lock without creating files or markers.
// A free lock means stopped at this instant, not readiness or a clean shutdown.
func ProbeOwnership(root string) (bool, error) {
	layout, err := Resolve(root)
	if err != nil {
		return false, err
	}
	if err = InspectRoot(layout.Root); err != nil {
		return false, err
	}
	fd, err := unix.Open(layout.Lock, unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_RDONLY, 0)
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(fd), layout.Lock)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if err = validateOwnerOnlyFile(info, layout.Lock); err != nil {
		return false, err
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); errors.Is(err, unix.EWOULDBLOCK) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, unix.Flock(fd, unix.LOCK_UN)
}

// ValidateSQLiteSidecar accepts SQLite's ambient read permissions only inside
// an owner-only directory. Shared writes, links and foreign owners still refuse.
// This is not an integrity check and never makes active WAL safe to ignore.
func ValidateSQLiteSidecar(path string) error {
	if !strings.HasSuffix(path, "-wal") && !strings.HasSuffix(path, "-shm") {
		return &ValidationError{path, "expected a SQLite WAL or SHM path"}
	}
	if err := InspectRoot(filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return &ValidationError{path, "expected a regular SQLite sidecar, not a symlink or another file type"}
	}
	if mode := info.Mode().Perm(); mode & ^os.FileMode(0644) != 0 || mode&0600 != 0600 {
		return &ValidationError{path, fmt.Sprintf("permissions are %04o; expected owner read/write with optional group/other read access (0600–0644)", mode)}
	}
	if err := validateOwner(info); err != nil {
		return &ValidationError{path, "SQLite sidecar must be owned by the current user"}
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
		return &ValidationError{path, "SQLite sidecar must have exactly one hard link"}
	}
	return nil
}

func isSQLiteSidecar(path string) bool {
	return strings.HasSuffix(path, ".db-wal") || strings.HasSuffix(path, ".db-shm")
}

func inspectStagingTree(root string, rootInfo os.FileInfo) error {
	device := rootInfo.Sys().(*syscall.Stat_t).Dev
	count := 0
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		count++
		if count > 100000 {
			return fmt.Errorf("migration tree exceeds inspection bound")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Dev != device {
			return &ValidationError{path, "installation tree crosses a filesystem"}
		}
		if entry.IsDir() {
			return validateOwnerOnlyDirectory(info, path)
		}
		if stat.Nlink != 1 {
			return &ValidationError{path, "installation file must have exactly one hard link"}
		}
		if isSQLiteSidecar(path) {
			return ValidateSQLiteSidecar(path)
		}
		return validateOwnerOnlyFile(info, path)
	})
}
