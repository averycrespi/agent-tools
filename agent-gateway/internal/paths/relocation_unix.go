//go:build darwin || linux

package paths

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
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
	if err = inspectRelocationTree(layout.Root, info); err != nil {
		return nil, err
	}
	lock, err := openRelocationLock(layout.Lock)
	if err != nil {
		return nil, err
	}
	return &Ownership{layout: layout, lock: lock}, nil
}

const relocationMagic = "agent-gateway relocation v1"

// Relocation holds the existing lock without establishing or clearing recovery markers.
// Callers must independently establish supervisor and process absence, and serialize
// installation management. No advisory lock protects against a hostile account owner.
type Relocation struct {
	Source, Destination string
	rootInfo            os.FileInfo
	parent              *os.File
	lock                *os.File
	reservation         *os.File
}

// InspectRelocation is read-only, including its nonblocking lock acquisition.
// Only sibling paths are supported: one atomic exchange and parent sync own cutover.
func InspectRelocation(source, destination string) (*Relocation, error) {
	for _, path := range []string{source, destination} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\n\r") {
			return nil, fmt.Errorf("%w: migration requires clean absolute paths", ErrUnsafePath)
		}
	}
	parentPath := filepath.Dir(source)
	if source == destination || parentPath != filepath.Dir(destination) {
		return nil, fmt.Errorf("%w: migration supports distinct sibling roots only; no copy fallback", ErrUnsafePath)
	}
	canonical, err := filepath.EvalSymlinks(parentPath)
	if err != nil || canonical != parentPath {
		return nil, fmt.Errorf("%w: migration parent must be canonical without links", ErrUnsafePath)
	}
	parentInfo, err := os.Lstat(parentPath)
	if err != nil {
		return nil, err
	}
	if err = validateOwner(parentInfo); err != nil {
		return nil, err
	}
	if !parentInfo.IsDir() || parentInfo.Mode().Perm()&0o022 != 0 {
		return nil, ErrUnsafePath
	}
	info, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	if err = validateOwnerOnlyDirectory(info, source); err != nil {
		return nil, err
	}
	if info.Sys().(*syscall.Stat_t).Dev != parentInfo.Sys().(*syscall.Stat_t).Dev {
		return nil, fmt.Errorf("%w: migration source is a separate filesystem", ErrUnsafePath)
	}
	parent, err := os.Open(parentPath)
	if err != nil {
		return nil, err
	}
	relocation := &Relocation{Source: source, Destination: destination, rootInfo: info, parent: parent}
	success := false
	defer func() {
		if !success {
			_ = relocation.Close()
		}
	}()
	relocation.lock, err = openRelocationLock(filepath.Join(source, LockName))
	if err != nil {
		return nil, err
	}
	if err = inspectRelocationTree(source, info); err != nil {
		return nil, err
	}
	// A matching reservation is retained after interruption and may be resumed only
	// by another explicitly confirmed call. Foreign/incomplete files remain blocked.
	if _, err = os.Lstat(destination); err == nil {
		if !matchesRelocationRecord(destination, source, destination, info) {
			return nil, fmt.Errorf("%w: destination exists and is not this migration's reservation", ErrUnsafePath)
		}
		relocation.reservation, err = openRelocationLock(destination)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	success = true
	return relocation, nil
}

func inspectRelocationTree(root string, rootInfo os.FileInfo) error {
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
			return fmt.Errorf("%w: migration tree crosses a filesystem", ErrUnsafePath)
		}
		if entry.IsDir() {
			return validateOwnerOnlyDirectory(info, path)
		}
		if stat.Nlink != 1 {
			return fmt.Errorf("%w: migration refuses hard-linked files", ErrUnsafePath)
		}
		return validateOwnerOnlyFile(info, path)
	})
}

func openRelocationLock(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err == nil {
		err = validateOwnerOnlyFile(info, path)
	}
	if err == nil {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	}
	if err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrInUse
		}
		return nil, err
	}
	return file, nil
}

func relocationRecord(source, destination string, info os.FileInfo) string {
	stat := info.Sys().(*syscall.Stat_t)
	return strings.Join([]string{relocationMagic, source, destination, fmt.Sprint(stat.Dev), strconv.FormatUint(stat.Ino, 10), ""}, "\n")
}

func matchesRelocationRecord(path, source, destination string, rootInfo os.FileInfo) bool {
	if ValidateOwnerOnlyFile(path) != nil {
		return false
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	return err == nil && len(data) <= 8192 && string(data) == relocationRecord(source, destination, rootInfo)
}

// RelocationCompleted recognizes only the exact moved directory inode, not a copy
// or arbitrary legacy file. The tombstone continues to block older binaries too.
func RelocationCompleted(source, destination string) bool {
	info, err := os.Lstat(destination)
	return err == nil && validateOwnerOnlyDirectory(info, destination) == nil && matchesRelocationRecord(source, source, destination, info)
}

// Commit atomically exchanges the whole directory and a blocking regular file.
// Every failure retains evidence; a sync failure never triggers compensation.
func (relocation *Relocation) Commit() error {
	if relocation.lock == nil {
		return ErrClosed
	}
	if err := relocation.revalidate(); err != nil {
		return err
	}
	if relocation.reservation == nil {
		file, err := CreateOwnerOnlyFile(relocation.Destination)
		if err != nil {
			return err
		}
		relocation.reservation = file
		if err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			return err
		}
		if _, err = file.WriteString(relocationRecord(relocation.Source, relocation.Destination, relocation.rootInfo)); err != nil {
			return err
		}
		if err = file.Sync(); err != nil {
			return err
		}
		if err = relocation.parent.Sync(); err != nil {
			return err
		}
	}
	if err := relocation.revalidate(); err != nil {
		return err
	}
	reserved, err := relocation.reservation.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(relocation.Destination)
	if err != nil || !os.SameFile(reserved, current) || !matchesRelocationRecord(relocation.Destination, relocation.Source, relocation.Destination, relocation.rootInfo) {
		return ErrUnsafePath
	}
	if err = exchangeRelocation(int(relocation.parent.Fd()), filepath.Base(relocation.Source), filepath.Base(relocation.Destination)); err != nil {
		return fmt.Errorf("atomic migration exchange refused; retain reservation and inspect before explicit resume: %w", err)
	}
	if err = relocation.parent.Sync(); err != nil {
		return fmt.Errorf("migration cutover durability uncertain; do not replay: %w", err)
	}
	if !RelocationCompleted(relocation.Source, relocation.Destination) {
		return fmt.Errorf("migration cutover identity uncertain; do not replay")
	}
	return nil
}

func (relocation *Relocation) revalidate() error {
	info, err := os.Lstat(relocation.Source)
	if err != nil || !os.SameFile(info, relocation.rootInfo) {
		return ErrUnsafePath
	}
	parentInfo, err := relocation.parent.Stat()
	if err != nil {
		return err
	}
	currentParent, err := os.Stat(filepath.Dir(relocation.Source))
	if err != nil || !os.SameFile(parentInfo, currentParent) {
		return ErrUnsafePath
	}
	lockInfo, err := relocation.lock.Stat()
	if err != nil {
		return err
	}
	currentLock, err := os.Lstat(filepath.Join(relocation.Source, LockName))
	if err != nil || !os.SameFile(lockInfo, currentLock) {
		return ErrUnsafePath
	}
	return inspectRelocationTree(relocation.Source, relocation.rootInfo)
}

func (relocation *Relocation) Close() error {
	var errs []error
	if relocation.reservation != nil {
		errs = append(errs, releaseFileLock(relocation.reservation))
		relocation.reservation = nil
	}
	if relocation.lock != nil {
		errs = append(errs, releaseFileLock(relocation.lock))
		relocation.lock = nil
	}
	if relocation.parent != nil {
		errs = append(errs, relocation.parent.Close())
		relocation.parent = nil
	}
	return errors.Join(errs...)
}
