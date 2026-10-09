package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
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

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
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
	FaultCopy          FaultPoint = "copy"
	FaultChecksum      FaultPoint = "checksum"
	FaultMetadata      FaultPoint = "metadata"
	FaultPublish       FaultPoint = "publish"
	FaultPublishedSync FaultPoint = "published_sync"
	FaultCleanup       FaultPoint = "cleanup"
)

type Clock interface{ Now() time.Time }

type Options struct {
	Diagnostics diagnostics.HTTPProxyObserver
	Ownership   *gatewaypaths.Ownership
	Store       *storage.Store
	Layout      gatewaypaths.Layout
	Clock       Clock
	Entropy     io.Reader
	Fault       func(FaultPoint) error
}

type Manager struct {
	ownership    *gatewaypaths.Ownership
	store        *storage.Store
	layout       gatewaypaths.Layout
	clock        Clock
	entropy      io.Reader
	fault        func(FaultPoint) error
	reserve      func(string, int64) (func(), error)
	work         chan struct{}
	mu           sync.RWMutex
	last         *string
	inventoryErr error
}

type artifactMetadata struct {
	Format             int    `json:"format,omitempty"`
	MasterKeyID        string `json:"master_key_id,omitempty"`
	TrafficGeneration  string `json:"traffic_generation,omitempty"`
	TrafficSHA256      string `json:"traffic_sha256,omitempty"`
	TrafficSizeBytes   int64  `json:"traffic_size_bytes,omitempty"`
	TrafficBudgetBytes int64  `json:"traffic_budget_bytes,omitempty"`
	contract.Backup
	AuthorityHash string `json:"authority_hash"`
	KeyHash       string `json:"key_hash"`
	InputHash     string `json:"input_hash"`
}

func New(options Options) (*Manager, error) {
	if options.Store == nil || options.Clock == nil || options.Entropy == nil || options.Layout.Backups == "" {
		return nil, errors.New("backup manager dependencies are incomplete")
	}
	manager := &Manager{ownership: options.Ownership, store: options.Store, layout: options.Layout, clock: options.Clock, entropy: options.Entropy, fault: options.Fault, work: make(chan struct{}, 1)}
	if err := ensureDirectory(options.Layout.Backups); err != nil {
		manager.inventoryErr = err
		observeInventory(options.Diagnostics, options.Layout.Backups, err)
		return manager, nil
	}
	items, _, err := manager.load(context.Background())
	if err != nil {
		manager.inventoryErr = err
		observeInventory(options.Diagnostics, options.Layout.Backups, err)
		return manager, nil
	}
	if len(items) > 0 {
		latest := items[len(items)-1].CreatedAt
		manager.last = &latest
	}
	return manager, nil
}

func (manager *Manager) Create(ctx context.Context, authorityID, idempotencyKey string) (result contract.Backup, replay bool, resultErr error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return contract.Backup{}, false, err
	}
	phase, artifactID, publication, directorySync, cleanupState, auditState := "custody verification", "none", "not_started", "not_started", "not_needed", "not_started"
	defer func() {
		if resultErr == nil {
			return
		}
		detail := diagnostics.Snapshot("backup", "create", manager.layout.Backups, resultErr, authorityID, idempotencyKey)
		detail.Explanation = fmt.Sprintf("backup_id=%s phase=%s publication=%s directory_sync=%s cleanup=%s outcome_audit=%s; %s", artifactID, phase, publication, directorySync, cleanupState, auditState, detail.Explanation)
		detail.Effect = "inspect artifact; reuse original idempotency key"
		resultErr = diagnostics.WithDetail(resultErr, detail)
	}()
	if err := manager.store.View(ctx, func(tx *sql.Tx) error { return requireBackupCustody(ctx, tx) }); err != nil {
		return contract.Backup{}, false, err
	}
	authorityHash, keyHash := digestText(authorityID), digestText(idempotencyKey)
	phase = "inventory and idempotency lookup"
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
			if metadata[index].Format != 4 {
				if err := manager.store.View(ctx, func(tx *sql.Tx) error {
					enabled, err := keyring.DatabaseCustodyTx(ctx, tx)
					if err != nil {
						return err
					}
					if enabled {
						return ErrEncryptedCustodyUnsupported
					}
					return nil
				}); err != nil {
					return contract.Backup{}, false, err
				}
			}
			verified, err := manager.Get(ctx, metadata[index].ID)
			return verified, err == nil, err
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
	phase = "reserve staging headroom"
	stageLimit, _ := contract.FixedLimitByName("database_bytes")
	// Control copy, compaction and WAL use the control limit, not traffic.
	headroom := 2 * stageLimit.Maximum
	releaseSpace, err := reserveHeadroom(manager.layout.Backups, 2*headroom, manager.reserve)
	if err != nil {
		return contract.Backup{}, false, errors.Join(ErrResourceLimit, err)
	}
	defer releaseSpace()
	id, err := admin.NewID(manager.clock.Now(), manager.entropy)
	if err != nil {
		return contract.Backup{}, false, withBackupCause("generate backup ID", err)
	}
	artifactID, phase = id, "attempt audit"
	attempt, err := audit.NewAttempt(ctx, manager.clock.Now(), "backup", "create", contract.AuditTarget{Type: "backup", ID: id})
	if err != nil {
		return contract.Backup{}, false, err
	}
	if err := audit.Append(ctx, manager.store, attempt); err != nil {
		return contract.Backup{}, false, err
	}
	settled := false
	defer func() {
		outcome := "unknown"
		if settled {
			outcome = "succeeded"
		}
		auditState = "unconfirmed"
		if err := audit.Finish(ctx, manager.store, attempt, manager.clock.Now(), outcome); err != nil {
			result, replay, resultErr = contract.Backup{}, false, errors.Join(resultErr, withBackupCause("finish backup audit", err))
		} else {
			auditState = "ack"
		}
	}()
	phase = "create staging directory"
	staging := filepath.Join(manager.layout.Backups, "."+id+".staging")
	published := filepath.Join(manager.layout.Backups, id)
	if err := os.Mkdir(staging, 0o700); err != nil {
		return contract.Backup{}, false, withBackupCause("create backup staging directory", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			cleanupState = "unconfirmed"
			cleanupErr := manager.fail(FaultCleanup)
			if cleanupErr == nil {
				cleanupErr = os.RemoveAll(staging)
			}
			if cleanupErr != nil {
				resultErr = errors.Join(resultErr, withBackupCause("remove staging directory", cleanupErr))
			} else {
				cleanupState = "removed"
			}
		}
	}()
	phase = "copy control database"
	if err := manager.fail(FaultCopy); err != nil {
		return contract.Backup{}, false, err
	}
	databasePath := filepath.Join(staging, databaseFile)
	if err := manager.store.BackupControlTo(ctx, databasePath, invocation.OmitLegacyTrafficTx); err != nil {
		return contract.Backup{}, false, err
	}
	phase = "verify staged database"
	identity, err := storage.VerifyBackup(ctx, databasePath)
	if err != nil {
		return contract.Backup{}, false, errors.Join(ErrInvalidArtifact, err)
	}
	if err := gitcredentials.VerifyBackup(ctx, databasePath, identity.SchemaVersion); err != nil {
		return contract.Backup{}, false, errors.Join(ErrInvalidArtifact, withBackupCause("Git configuration", err))
	}
	info, err := os.Stat(databasePath)
	if err != nil {
		return contract.Backup{}, false, withBackupCause("inspect backup database", err)
	}
	limit, _ := contract.FixedLimitByName("database_bytes")
	if info.Size() > limit.Maximum {
		return contract.Backup{}, false, ErrResourceLimit
	}
	phase = "checksum staged database"
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
	artifact.Format, artifact.History = 3, "omitted"
	if err := storage.ViewBackup(ctx, databasePath, func(tx *sql.Tx) error {
		encrypted, err := keyring.DatabaseCustodyTx(ctx, tx)
		if err != nil || !encrypted {
			return err
		}
		if manager.ownership == nil {
			return keyring.ErrCustodyUnavailable
		}
		custody, err := verifyEncryptedCustodyTx(ctx, tx, manager.ownership)
		if err != nil {
			return err
		}
		artifact.Format, artifact.MasterKeyID = 4, custody.KeyID
		return verifyEncryptedDomainsTx(ctx, tx)
	}); err != nil {
		return contract.Backup{}, false, err
	}
	phase = "write metadata"
	if err := manager.fail(FaultMetadata); err != nil {
		return contract.Backup{}, false, err
	}
	if err := writeMetadata(filepath.Join(staging, metadataFile), artifact); err != nil {
		return contract.Backup{}, false, err
	}
	phase = "sync staging directory"
	if err := syncDirectory(staging); err != nil {
		return contract.Backup{}, false, withBackupCause("sync staged backup", err)
	}
	phase = "publish rename"
	if err := manager.fail(FaultPublish); err != nil {
		return contract.Backup{}, false, err
	}
	if err := os.Rename(staging, published); err != nil {
		return contract.Backup{}, false, withBackupCause("publish backup", err)
	}
	cleanup = false
	publication, cleanupState, phase, directorySync = "renamed", "retained_published", "sync published directory", "unconfirmed"
	syncErr := manager.fail(FaultPublishedSync)
	if syncErr == nil {
		syncErr = syncDirectory(manager.layout.Backups)
	}
	if syncErr != nil {
		return contract.Backup{}, false, withBackupCause("sync published backup", syncErr)
	}
	directorySync, phase = "ack", "finish outcome audit"
	manager.mu.Lock()
	manager.last = &createdAt
	manager.mu.Unlock()
	settled = true
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
	if errors.Is(err, os.ErrNotExist) && !errors.Is(err, ErrInvalidArtifact) {
		return contract.Backup{}, diagnostics.WithDetail(ErrNotFound, diagnostics.Snapshot("backup", "get artifact", filepath.Join(manager.layout.Backups, id), err))
	}
	if err != nil {
		return contract.Backup{}, err
	}
	return metadata.Backup, nil
}

func (manager *Manager) Delete(ctx context.Context, id string) (resultErr error) {
	phase, removal, directorySync, auditState := "validate artifact", "not_started", "not_started", "not_started"
	defer func() {
		if resultErr == nil {
			return
		}
		detail := diagnostics.Snapshot("backup", "delete", manager.layout.Backups, resultErr)
		detail.Explanation = fmt.Sprintf("backup_id=%s phase=%s removal=%s directory_sync=%s outcome_audit=%s; %s", diagnostics.Text(id, 64), phase, removal, directorySync, auditState, detail.Explanation)
		detail.Effect = "inspect artifact before another delete"
		resultErr = diagnostics.WithDetail(resultErr, detail)
	}()
	if _, err := manager.Get(ctx, id); err != nil {
		return err
	}
	attempt, err := audit.NewAttempt(ctx, manager.clock.Now(), "backup", "delete", contract.AuditTarget{Type: "backup", ID: id})
	if err != nil {
		return err
	}
	if err := audit.Append(ctx, manager.store, attempt); err != nil {
		return err
	}
	defer func() {
		outcome := "unknown"
		if resultErr == nil {
			outcome = "succeeded"
		}
		auditState = "unconfirmed"
		auditErr := audit.Finish(ctx, manager.store, attempt, manager.clock.Now(), outcome)
		if auditErr == nil {
			auditState = "ack"
		}
		resultErr = errors.Join(resultErr, auditErr)
	}()
	path := filepath.Join(manager.layout.Backups, id)
	phase, removal = "remove artifact", "partial_or_unknown"
	if err := os.RemoveAll(path); err != nil {
		return withBackupCause("delete backup", err)
	}
	removal, phase, directorySync = "ack", "sync deletion directory", "unconfirmed"
	err = syncDirectory(manager.layout.Backups)
	if err == nil {
		directorySync, phase = "ack", "finish outcome audit"
	}
	return err
}

func (manager *Manager) WorkStatus() contract.LimitStatus {
	inUse := int64(len(manager.work))
	return contract.LimitStatus{InUse: inUse, Limit: 1, Saturated: inUse == 1}
}

func (manager *Manager) Status() contract.BackupStatus {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	state := contract.BackupIdle
	if manager.inventoryErr != nil {
		state = contract.BackupUnavailable
	}
	if len(manager.work) == 1 {
		state = contract.BackupCreating
	}
	return contract.BackupStatus{State: state, LastCompletedAt: manager.last}
}

func (manager *Manager) load(ctx context.Context) ([]contract.Backup, []artifactMetadata, error) {
	directory, err := openAccountingDirectory(manager.layout.Backups)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = directory.Close() }()
	items := make([]contract.Backup, 0)
	metadata := make([]artifactMetadata, 0)
	maximum, _ := contract.FixedLimitByName("backup_records")
	seen := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		entries, readErr := directory.ReadDir(64)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, nil, readErr
		}
		for _, entry := range entries {
			seen++
			if seen > 4096 {
				return nil, nil, artifactPredicate("inventory_entries", fmt.Sprintf("observed=%d allowed=4096", seen))
			}
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			if int64(len(items)) >= maximum.Maximum {
				return nil, nil, artifactPredicate("inventory_records", fmt.Sprintf("observed=%d allowed=%d", len(items)+1, maximum.Maximum))
			}
			if !entry.IsDir() {
				return nil, nil, artifactPredicate("inventory_entry_kind", "expected=directory observed=other")
			}
			if !backupIDPattern.MatchString(entry.Name()) {
				return nil, nil, artifactPredicate("inventory_entry_name", "expected=artifact_ID observed=invalid_name_withheld")
			}
			artifact, err := readAccountingMetadata(directory, entry.Name())
			if err != nil {
				return nil, nil, err
			}
			if _, err := time.Parse(time.RFC3339Nano, artifact.CreatedAt); err != nil {
				return nil, nil, artifactPredicate("metadata_created_at", "expected=RFC3339Nano observed=invalid")
			}
			items = append(items, artifact.Backup)
			metadata = append(metadata, artifact)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	sort.Slice(items, func(left, right int) bool { return items[left].ID < items[right].ID })
	sort.Slice(metadata, func(left, right int) bool { return metadata[left].ID < metadata[right].ID })
	return items, metadata, nil
}

func (manager *Manager) readArtifact(ctx context.Context, directory, id string) (artifactMetadata, error) {
	return manager.readArtifactScope(ctx, directory, id, false)
}

func (manager *Manager) readArtifactScope(ctx context.Context, directory, id string, securityOnly bool) (result artifactMetadata, resultErr error) {
	defer func() {
		if resultErr != nil {
			detail := diagnostics.Snapshot("backup", "verify artifact", directory, resultErr)
			detail.Effect = "preserve artifact; inspect metadata and local custody"
			resultErr = diagnostics.WithDetail(resultErr, detail)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := validateDirectory(directory); err != nil {
		return artifactMetadata{}, err
	}
	parent, err := openAccountingDirectory(filepath.Dir(directory))
	if err != nil {
		return artifactMetadata{}, err
	}
	metadata, readErr := readAccountingMetadata(parent, id)
	if err := errors.Join(readErr, parent.Close()); err != nil {
		return artifactMetadata{}, errors.Join(ErrInvalidArtifact, err)
	}
	if _, err := time.Parse(time.RFC3339Nano, metadata.CreatedAt); err != nil {
		return artifactMetadata{}, artifactPredicate("metadata_created_at", "expected=RFC3339Nano observed=invalid")
	}
	if err := requireClosedArtifactScope(directory, securityOnly || metadata.Format >= 3); err != nil {
		return artifactMetadata{}, err
	}
	databasePath := filepath.Join(directory, databaseFile)
	if err := gatewaypaths.ValidateOwnerOnlyFile(databasePath); err != nil {
		return artifactMetadata{}, errors.Join(ErrInvalidArtifact, err)
	}
	info, err := os.Stat(databasePath)
	if err != nil {
		return artifactMetadata{}, errors.Join(ErrInvalidArtifact, err)
	}
	if info.Size() != metadata.SizeBytes {
		return artifactMetadata{}, artifactPredicate("database_size", fmt.Sprintf("observed=%d expected=%d", info.Size(), metadata.SizeBytes))
	}
	digest, err := digestFile(databasePath)
	if err != nil {
		return artifactMetadata{}, errors.Join(ErrInvalidArtifact, err)
	}
	if digest != metadata.SHA256 {
		return artifactMetadata{}, artifactPredicate("database_checksum", "matches=false expected=metadata_checksum")
	}
	identity, err := storage.VerifyBackup(ctx, databasePath)
	if err != nil {
		return artifactMetadata{}, errors.Join(ErrInvalidArtifact, err)
	}
	if identity.InstallationID != metadata.InstallationID || fmt.Sprintf("%d", identity.SchemaVersion) != metadata.SchemaVersion || fmt.Sprintf("%d", identity.Revision) != metadata.SourceRevision {
		return artifactMetadata{}, artifactPredicate("database_identity", fmt.Sprintf("installation_matches=%t schema_matches=%t revision_matches=%t", identity.InstallationID == metadata.InstallationID, fmt.Sprintf("%d", identity.SchemaVersion) == metadata.SchemaVersion, fmt.Sprintf("%d", identity.Revision) == metadata.SourceRevision))
	}
	if err := gitcredentials.VerifyBackup(ctx, databasePath, identity.SchemaVersion); err != nil {
		return artifactMetadata{}, errors.Join(ErrInvalidArtifact, err)
	}
	if metadata.Format == 2 && identity.TrafficGeneration != metadata.TrafficGeneration {
		return artifactMetadata{}, ErrInvalidArtifact
	}
	if metadata.Format == 2 && !securityOnly {
		trafficPath := filepath.Join(directory, "traffic.db")
		if identity.TrafficGeneration != metadata.TrafficGeneration || metadata.TrafficGeneration == "" {
			return artifactMetadata{}, ErrInvalidArtifact
		}
		if err := invocation.VerifyTrafficFile(ctx, trafficPath, identity.InstallationID, metadata.TrafficGeneration, trafficConfig(metadata.TrafficBudgetBytes)); err != nil {
			return artifactMetadata{}, errors.Join(ErrInvalidArtifact, err)
		}
		info, err := os.Stat(trafficPath)
		if err != nil {
			return artifactMetadata{}, errors.Join(ErrInvalidArtifact, err)
		}
		if info.Size() != metadata.TrafficSizeBytes {
			return artifactMetadata{}, artifactPredicate("traffic_size", fmt.Sprintf("observed=%d expected=%d", info.Size(), metadata.TrafficSizeBytes))
		}
		digest, err := digestFile(trafficPath)
		if err != nil {
			return artifactMetadata{}, errors.Join(ErrInvalidArtifact, err)
		}
		if digest != metadata.TrafficSHA256 {
			return artifactMetadata{}, artifactPredicate("traffic_checksum", "matches=false expected=metadata_checksum")
		}
	} else if metadata.Format == 0 && identity.TrafficGeneration != "" {
		return artifactMetadata{}, ErrInvalidArtifact
	}
	if metadata.Format == 4 {
		if err := storage.ViewBackup(ctx, databasePath, func(tx *sql.Tx) error {
			custody, err := keyring.InspectBackupCustodyTx(ctx, tx)
			if err != nil {
				return err
			}
			if custody.KeyID != metadata.MasterKeyID {
				return artifactPredicate("custody_binding", "matches=false expected=metadata_master_key_identity")
			}
			return verifyEncryptedDomainsTx(ctx, tx)
		}); err != nil {
			return artifactMetadata{}, errors.Join(ErrInvalidArtifact, err)
		}
	}
	if metadata.Format >= 3 {
		if err := invocation.VerifyOmittedLegacyTraffic(ctx, databasePath); err != nil {
			return artifactMetadata{}, errors.Join(ErrInvalidArtifact, err)
		}
	}
	if metadata.Format == 0 && !securityOnly {
		if err := invocation.VerifyLegacyEvidence(ctx, databasePath, identity.SchemaVersion); err != nil {
			return artifactMetadata{}, errors.Join(ErrInvalidArtifact, err)
		}
	}
	if err := requireClosedArtifactScope(directory, securityOnly || metadata.Format >= 3); err != nil {
		return artifactMetadata{}, err
	}
	return metadata, nil
}

func reserveHeadroom(root string, bytes int64, reserve func(string, int64) (func(), error)) (func(), error) {
	if reserve == nil {
		reserve = gatewaypaths.ReserveHeadroom
	}
	return reserve(root, bytes)
}

func trafficConfig(budget int64) invocation.TrafficConfig {
	config := invocation.DefaultTrafficConfig()
	config.BudgetBytes = budget
	return config
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
		return withBackupCause("backup "+string(point)+" failed", err)
	}
	return nil
}

func ensureDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return withBackupCause("create backup directory", err)
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
		return withBackupCause("create backup metadata", err)
	}
	if err := json.NewEncoder(file).Encode(metadata); err != nil {
		_ = file.Close()
		return withBackupCause("write backup metadata", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return withBackupCause("sync backup metadata", err)
	}
	return file.Close()
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", withBackupCause("open backup for digest", err)
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", withBackupCause("digest backup", err)
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
