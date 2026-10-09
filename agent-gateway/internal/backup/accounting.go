package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

func readAccountingMetadata(directory *os.File, id string) (result artifactMetadata, resultErr error) {
	root, err := openAccountingChildDirectory(directory, id)
	if err != nil {
		return artifactMetadata{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	file, err := openAccountingFile(root, metadataFile)
	if err != nil {
		return artifactMetadata{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	var metadata artifactMetadata
	raw, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil {
		return artifactMetadata{}, err
	}
	if len(raw) > 8192 {
		return artifactMetadata{}, artifactPredicate("metadata_bytes", fmt.Sprintf("observed_prefix_bytes=%d allowed_bytes=8192", len(raw)))
	}
	if err := strictjson.Decode(raw, &metadata, strictjson.Options{MaxBytes: 8192, MaxDepth: 2, RejectUnknownMembers: true}); err != nil {
		return artifactMetadata{}, artifactPredicate("metadata_json", "expected=closed_unique_object_with_declared_types allowed_depth=2 malformed_details=withheld")
	}
	controlLimit, _ := contract.FixedLimitByName("database_bytes")
	if metadata.SizeBytes > controlLimit.Maximum {
		return artifactMetadata{}, artifactPredicate("metadata_database_bytes", fmt.Sprintf("observed=%d allowed=%d", metadata.SizeBytes, controlLimit.Maximum))
	}
	if metadata.ID != id {
		return artifactMetadata{}, artifactPredicate("metadata_artifact_identity", "matches=false expected=directory_ID")
	}
	if !backupIDPattern.MatchString(metadata.InstallationID) {
		return artifactMetadata{}, artifactPredicate("metadata_installation", "expected=installation_ID observed=invalid")
	}
	for _, field := range []struct{ name, value string }{{"sha256", metadata.SHA256}, {"authority_hash", metadata.AuthorityHash}, {"key_hash", metadata.KeyHash}, {"input_hash", metadata.InputHash}} {
		if !accountingDigest(field.value) {
			return artifactMetadata{}, artifactPredicate("metadata_digest", "field="+field.name+" expected=32_byte_hex observed=invalid")
		}
	}
	if metadata.SizeBytes <= 0 {
		return artifactMetadata{}, artifactPredicate("metadata_database_bytes", fmt.Sprintf("observed=%d expected=positive", metadata.SizeBytes))
	}
	schema, err := strconv.ParseUint(metadata.SchemaVersion, 10, 64)
	if err != nil || schema == 0 {
		return artifactMetadata{}, artifactPredicate("metadata_schema_version", "expected=positive_uint64_decimal observed=invalid")
	}
	if _, err := strconv.ParseUint(metadata.SourceRevision, 10, 64); err != nil {
		return artifactMetadata{}, artifactPredicate("metadata_source_revision", "expected=uint64_decimal observed=invalid")
	}
	if (metadata.Format == 4 && !accountingDigest(metadata.MasterKeyID)) || (metadata.Format != 4 && metadata.MasterKeyID != "") {
		return artifactMetadata{}, artifactPredicate("metadata_master_key", "expected=hex_identity_only_for_format_4 observed=invalid")
	}
	switch metadata.Format {
	case 3, 4:
		if metadata.History != "omitted" || metadata.TrafficGeneration != "" || metadata.TrafficSHA256 != "" || metadata.TrafficSizeBytes != 0 || metadata.TrafficBudgetBytes != 0 {
			return artifactMetadata{}, artifactPredicate("metadata_history", "expected=omitted_without_traffic_fields")
		}
	case 0:
		if metadata.History != "" || metadata.TrafficGeneration != "" || metadata.TrafficSHA256 != "" || metadata.TrafficSizeBytes != 0 || metadata.TrafficBudgetBytes != 0 {
			return artifactMetadata{}, artifactPredicate("metadata_history", "expected=legacy_empty_traffic_fields")
		}
	case 2:
		if metadata.History != "" || !backupIDPattern.MatchString(metadata.TrafficGeneration) || !accountingDigest(metadata.TrafficSHA256) || metadata.TrafficSizeBytes <= 0 || metadata.TrafficSizeBytes > metadata.TrafficBudgetBytes || metadata.TrafficBudgetBytes < 1<<20 || metadata.TrafficBudgetBytes > 16<<30 {
			return artifactMetadata{}, artifactPredicate("metadata_history", "expected=bound_generation_checksum_and_positive_bytes_within_1MiB..16GiB_budget")
		}
	default:
		return artifactMetadata{}, artifactPredicate("metadata_format", fmt.Sprintf("observed=%d allowed=0,2,3,4", metadata.Format))
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
