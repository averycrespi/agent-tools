package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

const retryDelay = 10 * time.Millisecond

type Lock struct{ lock *flock.Flock }

func TryAcquire(path string) (*Lock, bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, fmt.Errorf("creating lock directory: %w", err)
	}
	fileLock := flock.New(path)
	locked, err := fileLock.TryLock()
	if err != nil {
		return nil, false, fmt.Errorf("locking %s: %w", path, err)
	}
	if !locked {
		return nil, false, nil
	}
	return &Lock{lock: fileLock}, true, nil
}

func Acquire(ctx context.Context, path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating lock directory: %w", err)
	}
	fileLock := flock.New(path)
	locked, err := fileLock.TryLockContext(ctx, retryDelay)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	if !locked {
		return nil, context.Canceled
	}
	return &Lock{lock: fileLock}, nil
}

func (l *Lock) Unlock() error {
	if l == nil || l.lock == nil {
		return nil
	}
	return l.lock.Unlock()
}

type PostCommitError struct{ Err error }

func (e *PostCommitError) Error() string { return e.Err.Error() }
func (e *PostCommitError) Unwrap() error { return e.Err }

func CommitUncertain(err error) bool {
	var postCommit *PostCommitError
	return errors.As(err, &postCommit)
}

func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating private directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // private directories require owner traversal
		return fmt.Errorf("securing private directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting temporary file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncing temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	directory, err := os.Open(dir) //nolint:gosec // path is application-controlled state/config directory
	if err != nil {
		return &PostCommitError{Err: fmt.Errorf("opening parent directory: %w", err)}
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return &PostCommitError{Err: fmt.Errorf("syncing parent directory: %w", err)}
	}
	return nil
}

type ActionKey struct {
	Repository string `json:"repository"`
	Worktree   string `json:"worktree"`
	Trigger    string `json:"trigger"`
	Digest     string `json:"digest"`
}

type ActionResult struct {
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
	Attempted time.Time `json:"attempted"`
}

type Ledger struct {
	Version  int                     `json:"version"`
	Attempts map[string]ActionResult `json:"attempts"`
}

func LoadLedger(path string) (*Ledger, error) {
	data, err := os.ReadFile(path) //nolint:gosec // caller supplies the private action-ledger path
	if errors.Is(err, os.ErrNotExist) {
		return &Ledger{Version: 1, Attempts: make(map[string]ActionResult)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading action ledger: %w", err)
	}
	var ledger Ledger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return nil, fmt.Errorf("decoding action ledger: %w", err)
	}
	if ledger.Version != 1 || ledger.Attempts == nil {
		return nil, fmt.Errorf("unsupported action ledger version %d", ledger.Version)
	}
	return &ledger, nil
}

func actionKey(key ActionKey) string {
	data, _ := json.Marshal(key)
	return string(data)
}

func DecodeActionKey(value string) (ActionKey, error) {
	var key ActionKey
	if err := json.Unmarshal([]byte(value), &key); err != nil {
		return ActionKey{}, fmt.Errorf("decoding action key: %w", err)
	}
	if key.Repository == "" || key.Worktree == "" || key.Trigger == "" || key.Digest == "" {
		return ActionKey{}, fmt.Errorf("action key requires repository, worktree, trigger, and digest")
	}
	return key, nil
}

func (l *Ledger) Eligible(key ActionKey) bool {
	_, exists := l.Attempts[actionKey(key)]
	return !exists
}

func (l *Ledger) Record(key ActionKey, result ActionResult) {
	if l.Attempts == nil {
		l.Attempts = make(map[string]ActionResult)
	}
	if result.Attempted.IsZero() {
		result.Attempted = time.Now().UTC()
	}
	l.Attempts[actionKey(key)] = result
}

func (l *Ledger) Rerun(key ActionKey) { delete(l.Attempts, actionKey(key)) }

func (l *Ledger) Save(path string) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding action ledger: %w", err)
	}
	data = append(data, '\n')
	return AtomicWrite(path, data, 0o600)
}
