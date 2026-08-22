package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	DatabaseName       = "gateway.db"
	LockName           = "gateway.lock"
	RunMarkerName      = "run.unclean"
	MutationMarkerName = "mutation.intent"
	BackupsName        = "backups"
)

var (
	ErrUnsafePath = errors.New("unsafe installation path")
	ErrInUse      = errors.New("installation is already in use")
	ErrClosed     = errors.New("installation ownership is closed")
)

type Layout struct {
	Root           string
	Database       string
	Lock           string
	RunMarker      string
	MutationMarker string
	Backups        string
}

type Ownership struct {
	mu         sync.Mutex
	layout     Layout
	lock       *os.File
	wasUnclean bool
	clean      bool
	closed     bool
}

func Prepare(root string) (Layout, error) {
	canonicalRoot, err := prepareRoot(root)
	if err != nil {
		return Layout{}, err
	}
	return Layout{
		Root:           canonicalRoot,
		Database:       filepath.Join(canonicalRoot, DatabaseName),
		Lock:           filepath.Join(canonicalRoot, LockName),
		RunMarker:      filepath.Join(canonicalRoot, RunMarkerName),
		MutationMarker: filepath.Join(canonicalRoot, MutationMarkerName),
		Backups:        filepath.Join(canonicalRoot, BackupsName),
	}, nil
}

func Acquire(root string) (*Ownership, error) {
	layout, err := Prepare(root)
	if err != nil {
		return nil, err
	}
	lock, err := acquireFileLock(layout.Lock)
	if err != nil {
		return nil, err
	}
	ownership := &Ownership{layout: layout, lock: lock}
	if err := ownership.establishRunMarker(); err != nil {
		_ = releaseFileLock(lock)
		return nil, err
	}
	return ownership, nil
}

func AcquireForMaintenance(root string) (*Ownership, error) {
	return Acquire(root)
}

func ValidateOwnerOnlyFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect owner-only file: %w", err)
	}
	return validateOwnerOnlyFile(info, path)
}

func CreateOwnerOnlyFile(path string) (*os.File, error) {
	file, err := createOwnerOnlyFile(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect owner-only file: %w", err)
	}
	if err := validateOwnerOnlyFile(info, path); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func (ownership *Ownership) Layout() Layout {
	return ownership.layout
}

func (ownership *Ownership) ActiveLayout() (Layout, error) {
	ownership.mu.Lock()
	defer ownership.mu.Unlock()
	if ownership.closed {
		return Layout{}, ErrClosed
	}
	return ownership.layout, nil
}

func (ownership *Ownership) WasUnclean() bool {
	return ownership.wasUnclean
}

func (ownership *Ownership) MarkClean() error {
	ownership.mu.Lock()
	defer ownership.mu.Unlock()
	if ownership.closed {
		return ErrClosed
	}
	if ownership.clean {
		return nil
	}
	if err := os.Remove(ownership.layout.RunMarker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove run marker: %w", err)
	}
	if err := syncDirectory(ownership.layout.Root); err != nil {
		return fmt.Errorf("sync data root after removing run marker: %w", err)
	}
	ownership.clean = true
	return nil
}

func (ownership *Ownership) Close() error {
	ownership.mu.Lock()
	defer ownership.mu.Unlock()
	if ownership.closed {
		return nil
	}
	ownership.closed = true
	if err := releaseFileLock(ownership.lock); err != nil {
		return fmt.Errorf("release installation ownership: %w", err)
	}
	return nil
}

func (ownership *Ownership) establishRunMarker() error {
	info, err := os.Lstat(ownership.layout.RunMarker)
	switch {
	case err == nil:
		if err := validateOwnerOnlyFile(info, ownership.layout.RunMarker); err != nil {
			return err
		}
		ownership.wasUnclean = true
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("inspect run marker: %w", err)
	}

	marker, err := createOwnerOnlyFile(ownership.layout.RunMarker)
	if err != nil {
		return fmt.Errorf("create run marker: %w", err)
	}
	removeOnFailure := true
	defer func() {
		if removeOnFailure {
			_ = os.Remove(ownership.layout.RunMarker)
		}
	}()
	if _, err := marker.WriteString("running\n"); err != nil {
		_ = marker.Close()
		return fmt.Errorf("write run marker: %w", err)
	}
	if err := marker.Sync(); err != nil {
		_ = marker.Close()
		return fmt.Errorf("sync run marker: %w", err)
	}
	if err := marker.Close(); err != nil {
		return fmt.Errorf("close run marker: %w", err)
	}
	if err := syncDirectory(ownership.layout.Root); err != nil {
		return fmt.Errorf("sync data root after creating run marker: %w", err)
	}
	removeOnFailure = false
	return nil
}

func prepareRoot(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("%w: data root is empty", ErrUnsafePath)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve data root: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if info, statErr := os.Lstat(absolute); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: data root is a symlink", ErrUnsafePath)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect data root: %w", statErr)
	}

	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", fmt.Errorf("resolve data-root parent: %w", err)
	}
	canonical := filepath.Join(parent, filepath.Base(absolute))
	if err := os.Mkdir(canonical, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create data root: %w", err)
	}
	info, err := os.Lstat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect data root: %w", err)
	}
	if err := validateOwnerOnlyDirectory(info, canonical); err != nil {
		return "", err
	}
	return canonical, nil
}

func validateOwnerOnlyDirectory(info os.FileInfo, path string) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s is not a real directory", ErrUnsafePath, path)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: %s permissions are %04o, want 0700", ErrUnsafePath, path, info.Mode().Perm())
	}
	if err := validateOwner(info); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnsafePath, path, err)
	}
	return nil
}

func validateOwnerOnlyFile(info os.FileInfo, path string) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrUnsafePath, path)
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("%w: %s permissions are %04o, want 0600", ErrUnsafePath, path, info.Mode().Perm())
	}
	if err := validateOwner(info); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnsafePath, path, err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}
