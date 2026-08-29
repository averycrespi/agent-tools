package paths

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sync"
)

const (
	InstallationName   = "mcp-gateway"
	AdminBearerName    = "admin-bearer"
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
	AdminBearer    string
}

type Ownership struct {
	mu         sync.Mutex
	layout     Layout
	lock       *os.File
	wasUnclean bool
	clean      bool
	closed     bool
}

var currentUserHome = platformCurrentUserHome

func Resolve(explicitRoot string) (Layout, error) {
	return resolveInstallation(explicitRoot, os.Getenv("XDG_DATA_HOME"), currentUserHome)
}

func resolveInstallation(explicitRoot, xdgDataHome string, home func() (string, error)) (Layout, error) {
	var root string
	switch {
	case explicitRoot != "":
		absolute, err := filepath.Abs(explicitRoot)
		if err != nil {
			return Layout{}, fmt.Errorf("resolve explicit data directory: %w", err)
		}
		root = filepath.Clean(absolute)
	case xdgDataHome != "":
		if !filepath.IsAbs(xdgDataHome) {
			return Layout{}, fmt.Errorf("%w: XDG_DATA_HOME must be an absolute path", ErrUnsafePath)
		}
		root = filepath.Join(filepath.Clean(xdgDataHome), InstallationName)
	default:
		homeDirectory, err := home()
		if err != nil {
			return Layout{}, fmt.Errorf("resolve current user home: %w", err)
		}
		if !filepath.IsAbs(homeDirectory) {
			return Layout{}, fmt.Errorf("%w: current user home must be an absolute path", ErrUnsafePath)
		}
		root = filepath.Join(filepath.Clean(homeDirectory), ".local", "share", InstallationName)
	}
	return layoutForRoot(root), nil
}

func platformCurrentUserHome() (string, error) {
	account, err := user.Current()
	if err != nil {
		return "", err
	}
	if account.HomeDir == "" {
		return "", errors.New("current user account has no home directory")
	}
	return account.HomeDir, nil
}

func Prepare(root string) (Layout, error) {
	canonicalRoot, err := prepareRoot(root)
	if err != nil {
		return Layout{}, err
	}
	return layoutForRoot(canonicalRoot), nil
}

func layoutForRoot(root string) Layout {
	return Layout{
		Root:           root,
		Database:       filepath.Join(root, DatabaseName),
		Lock:           filepath.Join(root, LockName),
		RunMarker:      filepath.Join(root, RunMarkerName),
		MutationMarker: filepath.Join(root, MutationMarkerName),
		Backups:        filepath.Join(root, BackupsName),
		AdminBearer:    filepath.Join(root, AdminBearerName),
	}
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

	current := absolute
	missing := make([]string, 0, 3)
	for {
		info, statErr := os.Lstat(current)
		switch {
		case statErr == nil:
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return "", fmt.Errorf("%w: %s is not a real directory", ErrUnsafePath, current)
			}
			if err := validateOwner(info); err != nil {
				return "", fmt.Errorf("%w: %s: %w", ErrUnsafePath, current, err)
			}
			canonical, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", fmt.Errorf("resolve existing data-root ancestor: %w", err)
			}
			for index := len(missing) - 1; index >= 0; index-- {
				canonical = filepath.Join(canonical, missing[index])
				if err := os.Mkdir(canonical, 0o700); err != nil {
					return "", fmt.Errorf("create data-root component: %w", err)
				}
				createdInfo, err := os.Lstat(canonical)
				if err != nil {
					return "", fmt.Errorf("inspect data-root component: %w", err)
				}
				if err := validateOwnerOnlyDirectory(createdInfo, canonical); err != nil {
					return "", err
				}
			}
			rootInfo, err := os.Lstat(canonical)
			if err != nil {
				return "", fmt.Errorf("inspect data root: %w", err)
			}
			if err := validateOwnerOnlyDirectory(rootInfo, canonical); err != nil {
				return "", err
			}
			return canonical, nil
		case errors.Is(statErr, os.ErrNotExist):
			parent := filepath.Dir(current)
			if parent == current {
				return "", fmt.Errorf("%w: no owner-controlled data-root ancestor", ErrUnsafePath)
			}
			missing = append(missing, filepath.Base(current))
			current = parent
		default:
			return "", fmt.Errorf("inspect data-root component: %w", statErr)
		}
	}
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
