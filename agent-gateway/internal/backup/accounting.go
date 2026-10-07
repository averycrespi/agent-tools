package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

// AccountingStatus reports metadata occupancy, not artifact integrity. Both
// counts share one traversal and one retention instant; no database is opened.
func (manager *Manager) AccountingStatus(ctx context.Context) (records, idempotency contract.LimitStatus, resultErr error) {
	recordLimit, _ := contract.FixedLimitByName("backup_records")
	retryLimit, _ := contract.FixedLimitByName("idempotency_records")
	records.Limit, idempotency.Limit = recordLimit.Maximum, retryLimit.Maximum
	defer func() {
		manager.mu.Lock()
		manager.inventoryErr = resultErr
		manager.mu.Unlock()
		if resultErr != nil {
			records, idempotency = contract.LimitStatus{}, contract.LimitStatus{}
			resultErr = errors.Join(ErrInvalidArtifact, resultErr)
		}
	}()
	directory, err := openAccountingDirectory(manager.layout.Backups)
	if err != nil {
		return records, idempotency, err
	}
	defer func() { _ = directory.Close() }()
	now := manager.clock.Now()
	seen := 0
	for {
		if err := ctx.Err(); err != nil {
			return records, idempotency, err
		}
		entries, readErr := directory.ReadDir(64)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return records, idempotency, readErr
		}
		for _, entry := range entries {
			seen++
			if seen > 4096 {
				return records, idempotency, ErrInvalidArtifact
			}
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			if records.InUse >= records.Limit {
				return records, idempotency, ErrInvalidArtifact
			}
			if !entry.IsDir() || !backupIDPattern.MatchString(entry.Name()) {
				return records, idempotency, ErrInvalidArtifact
			}
			metadata, err := readAccountingMetadata(directory, entry.Name())
			if err != nil {
				return records, idempotency, err
			}
			records.InUse++
			created, err := time.Parse(time.RFC3339Nano, metadata.CreatedAt)
			if err != nil {
				return records, idempotency, ErrInvalidArtifact
			}
			if now.Sub(created) <= contract.IdempotencyRetention {
				idempotency.InUse++
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	records.Saturated = records.InUse >= records.Limit
	idempotency.Saturated = idempotency.InUse >= idempotency.Limit
	return records, idempotency, nil
}

func readAccountingMetadata(directory *os.File, id string) (artifactMetadata, error) {
	root, err := openAccountingChildDirectory(directory, id)
	if err != nil {
		return artifactMetadata{}, err
	}
	defer func() { _ = root.Close() }()
	file, err := openAccountingFile(root, metadataFile)
	if err != nil {
		return artifactMetadata{}, err
	}
	defer func() { _ = file.Close() }()
	var metadata artifactMetadata
	if err := strictjson.DecodeReader(file, &metadata, strictjson.Options{MaxBytes: 8192, MaxDepth: 2, RejectUnknownMembers: true}); err != nil {
		return artifactMetadata{}, err
	}
	controlLimit, _ := contract.FixedLimitByName("database_bytes")
	if metadata.SizeBytes > controlLimit.Maximum {
		return artifactMetadata{}, ErrInvalidArtifact
	}
	if metadata.ID != id || !backupIDPattern.MatchString(metadata.InstallationID) || !accountingDigest(metadata.SHA256) || !accountingDigest(metadata.AuthorityHash) || !accountingDigest(metadata.KeyHash) || !accountingDigest(metadata.InputHash) || metadata.SizeBytes <= 0 {
		return artifactMetadata{}, ErrInvalidArtifact
	}
	schema, err := strconv.ParseUint(metadata.SchemaVersion, 10, 64)
	if err != nil || schema == 0 {
		return artifactMetadata{}, ErrInvalidArtifact
	}
	if _, err := strconv.ParseUint(metadata.SourceRevision, 10, 64); err != nil {
		return artifactMetadata{}, ErrInvalidArtifact
	}
	if (metadata.Format == 4 && !accountingDigest(metadata.MasterKeyID)) || (metadata.Format != 4 && metadata.MasterKeyID != "") {
		return artifactMetadata{}, ErrInvalidArtifact
	}
	switch metadata.Format {
	case 3, 4:
		if metadata.History != "omitted" || metadata.TrafficGeneration != "" || metadata.TrafficSHA256 != "" || metadata.TrafficSizeBytes != 0 || metadata.TrafficBudgetBytes != 0 {
			return artifactMetadata{}, ErrInvalidArtifact
		}
	case 0:
		if metadata.History != "" || metadata.TrafficGeneration != "" || metadata.TrafficSHA256 != "" || metadata.TrafficSizeBytes != 0 || metadata.TrafficBudgetBytes != 0 {
			return artifactMetadata{}, ErrInvalidArtifact
		}
	case 2:
		if metadata.History != "" || !backupIDPattern.MatchString(metadata.TrafficGeneration) || !accountingDigest(metadata.TrafficSHA256) || metadata.TrafficSizeBytes <= 0 || metadata.TrafficSizeBytes > metadata.TrafficBudgetBytes || metadata.TrafficBudgetBytes < 1<<20 || metadata.TrafficBudgetBytes > 16<<30 {
			return artifactMetadata{}, ErrInvalidArtifact
		}
	default:
		return artifactMetadata{}, ErrInvalidArtifact
	}
	return metadata, nil
}

func accountingDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
