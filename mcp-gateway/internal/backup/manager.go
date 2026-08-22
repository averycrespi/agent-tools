package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

const (
	databaseFile = "gateway.db"
	metadataFile = "metadata.json"
)

var (
	ErrInvalidArtifact    = errors.New("invalid backup artifact")
	ErrNotFound           = errors.New("backup not found")
	ErrResourceLimit      = errors.New("backup resource limit reached")
	ErrInvalidIdempotency = errors.New("invalid idempotency key")
	backupIDPattern       = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
)

type FaultPoint string

const (
	FaultCopy     FaultPoint = "copy"
	FaultChecksum FaultPoint = "checksum"
	FaultMetadata FaultPoint = "metadata"
	FaultPublish  FaultPoint = "publish"
)

type Clock interface{ Now() time.Time }

type Options struct {
	Store   *storage.Store
	Layout  gatewaypaths.Layout
	Clock   Clock
	Entropy io.Reader
	Fault   func(FaultPoint) error
}

type Manager struct {
	store   *storage.Store
	layout  gatewaypaths.Layout
	clock   Clock
	entropy io.Reader
	fault   func(FaultPoint) error
	work    chan struct{}
	mu      sync.RWMutex
	last    *string
}

type artifactMetadata struct {
	contract.Backup
	AuthorityHash string `json:"authority_hash"`
	KeyHash       string `json:"key_hash"`
	InputHash     string `json:"input_hash"`
}

func New(options Options) (*Manager, error) {
	if options.Store == nil || options.Clock == nil || options.Entropy == nil || options.Layout.Backups == "" {
		return nil, errors.New("backup manager dependencies are incomplete")
	}
	if err := ensureDirectory(options.Layout.Backups); err != nil {
		return nil, err
	}
	manager := &Manager{store: options.Store, layout: options.Layout, clock: options.Clock, entropy: options.Entropy, fault: options.Fault, work: make(chan struct{}, 1)}
	items, _, err := manager.load(context.Background())
	if err != nil {
		return nil, err
	}
	if len(items) > 0 {
		latest := items[len(items)-1].CreatedAt
		manager.last = &latest
	}
	return manager, nil
}

func (manager *Manager) Create(ctx context.Context, authorityID, idempotencyKey string) (contract.Backup, bool, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return contract.Backup{}, false, err
	}
	authorityHash, keyHash := digestText(authorityID), digestText(idempotencyKey)
	items, metadata, err := manager.load(ctx)
	if err != nil {
		return contract.Backup{}, false, err
	}
	for index := range metadata {
		createdAt, parseErr := time.Parse(time.RFC3339Nano, metadata[index].CreatedAt)
		if parseErr != nil {
			return contract.Backup{}, false, ErrInvalidArtifact
		}
		if metadata[index].AuthorityHash == authorityHash && metadata[index].KeyHash == keyHash && manager.clock.Now().Sub(createdAt) <= contract.IdempotencyRetention {
			if metadata[index].InputHash != digestText("{}") {
				return contract.Backup{}, false, ErrInvalidIdempotency
			}
			return metadata[index].Backup, true, nil
		}
	}
	maximum, _ := contract.FixedLimitByName("backup_records")
	if int64(len(items)) >= maximum.Maximum {
		return contract.Backup{}, false, ErrResourceLimit
	}
	select {
	case manager.work <- struct{}{}:
		defer func() { <-manager.work }()
	default:
		return contract.Backup{}, false, ErrResourceLimit
	}
	id, err := admin.NewID(manager.clock.Now(), manager.entropy)
	if err != nil {
		return contract.Backup{}, false, fmt.Errorf("generate backup ID: %w", err)
	}
	staging := filepath.Join(manager.layout.Backups, "."+id+".staging")
	published := filepath.Join(manager.layout.Backups, id)
	if err := os.Mkdir(staging, 0o700); err != nil {
		return contract.Backup{}, false, fmt.Errorf("create backup staging directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := manager.fail(FaultCopy); err != nil {
		return contract.Backup{}, false, err
	}
	databasePath := filepath.Join(staging, databaseFile)
	if err := manager.store.BackupTo(ctx, databasePath); err != nil {
		return contract.Backup{}, false, err
	}
	identity, err := storage.VerifyBackup(ctx, databasePath)
	if err != nil {
		return contract.Backup{}, false, fmt.Errorf("%w: %w", ErrInvalidArtifact, err)
	}
	info, err := os.Stat(databasePath)
	if err != nil {
		return contract.Backup{}, false, fmt.Errorf("inspect backup database: %w", err)
	}
	limit, _ := contract.FixedLimitByName("database_bytes")
	if info.Size() > limit.Maximum {
		return contract.Backup{}, false, ErrResourceLimit
	}
	if err := manager.fail(FaultChecksum); err != nil {
		return contract.Backup{}, false, err
	}
	digest, err := digestFile(databasePath)
	if err != nil {
		return contract.Backup{}, false, err
	}
	createdAt := manager.clock.Now().UTC().Format(time.RFC3339Nano)
	artifact := artifactMetadata{
		Backup:        contract.Backup{ID: id, CreatedAt: createdAt, InstallationID: identity.InstallationID, SchemaVersion: fmt.Sprintf("%d", identity.SchemaVersion), SourceRevision: fmt.Sprintf("%d", identity.Revision), SizeBytes: info.Size(), SHA256: digest},
		AuthorityHash: authorityHash, KeyHash: keyHash, InputHash: digestText("{}"),
	}
	if err := manager.fail(FaultMetadata); err != nil {
		return contract.Backup{}, false, err
	}
	if err := writeMetadata(filepath.Join(staging, metadataFile), artifact); err != nil {
		return contract.Backup{}, false, err
	}
	if err := syncDirectory(staging); err != nil {
		return contract.Backup{}, false, fmt.Errorf("sync staged backup: %w", err)
	}
	if err := manager.fail(FaultPublish); err != nil {
		return contract.Backup{}, false, err
	}
	if err := os.Rename(staging, published); err != nil {
		return contract.Backup{}, false, fmt.Errorf("publish backup: %w", err)
	}
	cleanup = false
	if err := syncDirectory(manager.layout.Backups); err != nil {
		return contract.Backup{}, false, fmt.Errorf("sync published backup: %w", err)
	}
	manager.mu.Lock()
	manager.last = &createdAt
	manager.mu.Unlock()
	return artifact.Backup, false, nil
}

func (manager *Manager) List(ctx context.Context) ([]contract.Backup, error) {
	items, _, err := manager.load(ctx)
	return items, err
}

func (manager *Manager) Get(ctx context.Context, id string) (contract.Backup, error) {
	if !backupIDPattern.MatchString(id) {
		return contract.Backup{}, ErrNotFound
	}
	metadata, err := manager.readArtifact(ctx, filepath.Join(manager.layout.Backups, id), id)
	if errors.Is(err, os.ErrNotExist) {
		return contract.Backup{}, ErrNotFound
	}
	if err != nil {
		return contract.Backup{}, err
	}
	return metadata.Backup, nil
}

func (manager *Manager) Delete(ctx context.Context, id string) error {
	if _, err := manager.Get(ctx, id); err != nil {
		return err
	}
	path := filepath.Join(manager.layout.Backups, id)
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("delete backup: %w", err)
	}
	return syncDirectory(manager.layout.Backups)
}

func (manager *Manager) WorkStatus() contract.LimitStatus {
	inUse := int64(len(manager.work))
	return contract.LimitStatus{InUse: inUse, Limit: 1, Saturated: inUse == 1}
}

func (manager *Manager) RecordStatus() contract.LimitStatus {
	items, err := manager.List(context.Background())
	inUse := int64(len(items))
	if err != nil {
		inUse = 0
	}
	limit, _ := contract.FixedLimitByName("backup_records")
	return contract.LimitStatus{InUse: inUse, Limit: limit.Maximum, Saturated: inUse >= limit.Maximum}
}

func (manager *Manager) IdempotencyStatus() contract.LimitStatus {
	_, metadata, err := manager.load(context.Background())
	var inUse int64
	if err == nil {
		for _, item := range metadata {
			createdAt, parseErr := time.Parse(time.RFC3339Nano, item.CreatedAt)
			if parseErr == nil && manager.clock.Now().Sub(createdAt) <= contract.IdempotencyRetention {
				inUse++
			}
		}
	}
	limit, _ := contract.FixedLimitByName("idempotency_records")
	return contract.LimitStatus{InUse: inUse, Limit: limit.Maximum, Saturated: inUse >= limit.Maximum}
}

func (manager *Manager) Status() contract.BackupStatus {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	state := contract.BackupIdle
	if len(manager.work) == 1 {
		state = contract.BackupCreating
	}
	return contract.BackupStatus{State: state, LastCompletedAt: manager.last}
}

func (manager *Manager) load(ctx context.Context) ([]contract.Backup, []artifactMetadata, error) {
	entries, err := os.ReadDir(manager.layout.Backups)
	if err != nil {
		return nil, nil, fmt.Errorf("read backups: %w", err)
	}
	items := make([]contract.Backup, 0, len(entries))
	metadata := make([]artifactMetadata, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if !entry.IsDir() || !backupIDPattern.MatchString(entry.Name()) {
			return nil, nil, ErrInvalidArtifact
		}
		artifact, readErr := manager.readArtifact(ctx, filepath.Join(manager.layout.Backups, entry.Name()), entry.Name())
		if readErr != nil {
			return nil, nil, readErr
		}
		items = append(items, artifact.Backup)
		metadata = append(metadata, artifact)
	}
	sort.Slice(items, func(left, right int) bool { return items[left].ID < items[right].ID })
	sort.Slice(metadata, func(left, right int) bool { return metadata[left].ID < metadata[right].ID })
	return items, metadata, nil
}

func (manager *Manager) readArtifact(ctx context.Context, directory, id string) (artifactMetadata, error) {
	if err := validateDirectory(directory); err != nil {
		return artifactMetadata{}, err
	}
	contents, err := os.ReadFile(filepath.Join(directory, metadataFile))
	if err != nil {
		return artifactMetadata{}, err
	}
	var metadata artifactMetadata
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil || decoder.Decode(new(any)) != io.EOF || metadata.ID != id ||
		len(metadata.AuthorityHash) != sha256.Size*2 || len(metadata.KeyHash) != sha256.Size*2 || len(metadata.InputHash) != sha256.Size*2 {
		return artifactMetadata{}, ErrInvalidArtifact
	}
	if _, err := time.Parse(time.RFC3339Nano, metadata.CreatedAt); err != nil {
		return artifactMetadata{}, ErrInvalidArtifact
	}
	databasePath := filepath.Join(directory, databaseFile)
	identity, err := storage.VerifyBackup(ctx, databasePath)
	if err != nil || identity.InstallationID != metadata.InstallationID || fmt.Sprintf("%d", identity.SchemaVersion) != metadata.SchemaVersion || fmt.Sprintf("%d", identity.Revision) != metadata.SourceRevision {
		return artifactMetadata{}, ErrInvalidArtifact
	}
	info, err := os.Stat(databasePath)
	if err != nil || info.Size() != metadata.SizeBytes {
		return artifactMetadata{}, ErrInvalidArtifact
	}
	digest, err := digestFile(databasePath)
	if err != nil || digest != metadata.SHA256 {
		return artifactMetadata{}, ErrInvalidArtifact
	}
	return metadata, nil
}

func ValidID(value string) bool { return backupIDPattern.MatchString(value) }

func validateIdempotencyKey(value string) error {
	if len(value) < 1 || len(value) > 128 {
		return ErrInvalidIdempotency
	}
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e {
			return ErrInvalidIdempotency
		}
	}
	return nil
}

func (manager *Manager) fail(point FaultPoint) error {
	if manager.fault == nil {
		return nil
	}
	if err := manager.fault(point); err != nil {
		return fmt.Errorf("backup %s failed: %w", point, err)
	}
	return nil
}

func ensureDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create backup directory: %w", err)
	}
	return validateDirectory(path)
}

func validateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return ErrInvalidArtifact
	}
	return nil
}

func writeMetadata(path string, metadata artifactMetadata) error {
	file, err := gatewaypaths.CreateOwnerOnlyFile(path)
	if err != nil {
		return fmt.Errorf("create backup metadata: %w", err)
	}
	if err := json.NewEncoder(file).Encode(metadata); err != nil {
		_ = file.Close()
		return fmt.Errorf("write backup metadata: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync backup metadata: %w", err)
	}
	return file.Close()
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open backup for digest: %w", err)
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("digest backup: %w", err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func digestText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
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
