package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

type Clock interface {
	Now() time.Time
}

type CommitFence struct {
	ServerID                     string
	ExpectedDesiredRevision      string
	ExpectedRegistrationRevision string
	ExpectedCredentialRevisions  contract.CredentialRevisions
	ExpectedCatalogRevision      string
}

type DurableStatus struct {
	State         contract.DurableCatalogState
	Revision      *string
	ToolCount     int64
	IssueCount    int64
	LastSuccessAt *string
}

type DescriptorCursor struct {
	ServerID        string                           `json:"server_id"`
	Retired         contract.DescriptorRetiredFilter `json:"retired"`
	CatalogRevision string                           `json:"catalog_revision"`
	Upper           int64                            `json:"upper"`
	After           int64                            `json:"after"`
	AfterID         string                           `json:"after_id"`
}

type DescriptorRecord struct {
	InsertionSequence int64
	Resource          contract.ToolDescriptor
}

type DescriptorPage struct {
	Items []DescriptorRecord
	Next  *DescriptorCursor
}

type Repository struct {
	store   *storage.Store
	clock   Clock
	entropy io.Reader
}

func NewRepository(store *storage.Store, clock Clock, entropy io.Reader) (*Repository, error) {
	if store == nil || clock == nil || entropy == nil {
		return nil, errors.New("catalog repository dependencies are incomplete")
	}
	return &Repository{store: store, clock: clock, entropy: entropy}, nil
}

func (repository *Repository) Commit(ctx context.Context, fence CommitFence, candidate NormalizedCandidate) (DurableStatus, error) {
	if fence.ServerID == "" || fence.ExpectedDesiredRevision == "" || fence.ExpectedRegistrationRevision == "" || fence.ExpectedCatalogRevision == "" {
		return DurableStatus{}, servers.ErrStaleRevision
	}
	if int64(len(candidate.Tools)) > fixedLimit("active_tools_per_server") {
		return DurableStatus{}, servers.ErrResourceLimit
	}
	validated := make([]NormalizedTool, len(candidate.Tools))
	seen := make(map[string]struct{}, len(candidate.Tools))
	for index, tool := range candidate.Tools {
		if tool.Key.ServerID != fence.ServerID {
			return DurableStatus{}, servers.ErrInvalidInput
		}
		if _, duplicate := seen[tool.Key.UpstreamName]; duplicate {
			return DurableStatus{}, servers.ErrInvalidInput
		}
		seen[tool.Key.UpstreamName] = struct{}{}
		rebuilt, err := NormalizeTool(RawTool{UpstreamName: tool.Key.UpstreamName, ExternalName: tool.ExternalName, Descriptor: tool.Canonical}, NormalizeOptions{ServerID: fence.ServerID, AllowHeaderBindings: true})
		if err != nil || rebuilt.Fingerprint != tool.Fingerprint || !bytes.Equal(rebuilt.Canonical, tool.Canonical) {
			return DurableStatus{}, servers.ErrInvalidInput
		}
		validated[index] = rebuilt
	}
	for _, issue := range candidate.Issues {
		if issue != IssueDescriptorInvalid {
			return DurableStatus{}, servers.ErrInvalidInput
		}
	}
	issueCount := int64(len(candidate.Issues))
	var status DurableStatus
	err := repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		if err := validateCommitFence(ctx, transaction, fence); err != nil {
			return err
		}
		current, err := statusTx(ctx, transaction, fence.ServerID)
		if err != nil {
			return err
		}
		currentRevision := "0"
		if current.Revision != nil {
			currentRevision = *current.Revision
		}
		if currentRevision != fence.ExpectedCatalogRevision {
			return servers.ErrStaleRevision
		}
		newNames, err := missingIdentityNames(ctx, transaction, fence.ServerID, validated)
		if err != nil {
			return err
		}
		if err := checkIdentityCapacity(ctx, transaction, fence.ServerID, int64(len(newNames))); err != nil {
			return err
		}
		now := repository.clock.Now().UTC().Format(time.RFC3339Nano)
		for _, name := range newNames {
			id, err := admin.NewID(repository.clock.Now(), repository.entropy)
			if err != nil {
				return err
			}
			tool := toolByName(validated, name)
			if _, err := transaction.ExecContext(ctx, `
				INSERT INTO durable_tool_identities
					(id, server_id, upstream_name, external_name, first_seen_at)
				VALUES (?, ?, ?, ?, ?)`, id, fence.ServerID, name, tool.ExternalName, now); err != nil {
				return fmt.Errorf("insert durable tool identity: %w", err)
			}
		}
		currentNumber, err := strconv.ParseInt(currentRevision, 10, 64)
		if err != nil || currentNumber == int64(^uint64(0)>>1) {
			return servers.ErrStaleRevision
		}
		next := currentNumber + 1
		if _, err := transaction.ExecContext(ctx, `
			UPDATE tool_descriptors SET retired_at = ?, catalog_revision = ?
			WHERE tool_id IN (
				SELECT id FROM durable_tool_identities WHERE server_id = ?
			) AND retired_at IS NULL`, now, next, fence.ServerID); err != nil {
			return fmt.Errorf("retire durable descriptors: %w", err)
		}
		for _, tool := range validated {
			var toolID string
			if err := transaction.QueryRowContext(ctx, `
				SELECT id FROM durable_tool_identities
				WHERE server_id = ? AND upstream_name = ?`, fence.ServerID, tool.Key.UpstreamName).Scan(&toolID); err != nil {
				return err
			}
			if _, err := transaction.ExecContext(ctx, `
				INSERT INTO tool_descriptors (
					tool_id, descriptor_json, fingerprint, catalog_revision, last_seen_at, retired_at
				) VALUES (?, ?, ?, ?, ?, NULL)
				ON CONFLICT (tool_id) DO UPDATE SET
					descriptor_json = excluded.descriptor_json,
					fingerprint = excluded.fingerprint,
					catalog_revision = excluded.catalog_revision,
					last_seen_at = excluded.last_seen_at,
					retired_at = NULL`, toolID, string(tool.Canonical), tool.Fingerprint, next, now); err != nil {
				return fmt.Errorf("publish durable descriptor: %w", err)
			}
		}
		if _, err := transaction.ExecContext(ctx, `DELETE FROM server_catalog_issues WHERE server_id = ?`, fence.ServerID); err != nil {
			return err
		}
		if issueCount != 0 {
			if _, err := transaction.ExecContext(ctx, `
				INSERT INTO server_catalog_issues
					(server_id, catalog_revision, issue_class, occurrences)
				VALUES (?, ?, 'descriptor_invalid', ?)`, fence.ServerID, next, issueCount); err != nil {
				return err
			}
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE server_catalogs SET
				durable_revision = ?, durable_state = 'current',
				durable_tool_count = ?, issue_count = ?, last_success_at = ?
			WHERE server_id = ?`, next, len(validated), issueCount, now, fence.ServerID); err != nil {
			return fmt.Errorf("publish durable catalog status: %w", err)
		}
		status, err = statusTx(ctx, transaction, fence.ServerID)
		return err
	})
	return status, mapStoreError(err)
}

func (repository *Repository) SetState(ctx context.Context, fence CommitFence, state contract.DurableCatalogState, issueCount int64) (DurableStatus, error) {
	if state != contract.DurableCatalogStale && state != contract.DurableCatalogUnavailable && state != contract.DurableCatalogRetired || issueCount < 0 {
		return DurableStatus{}, servers.ErrInvalidInput
	}
	var status DurableStatus
	err := repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		if err := validateCommitFence(ctx, transaction, fence); err != nil {
			return err
		}
		current, err := statusTx(ctx, transaction, fence.ServerID)
		if err != nil {
			return err
		}
		currentRevision := "0"
		if current.Revision != nil {
			currentRevision = *current.Revision
		}
		if currentRevision != fence.ExpectedCatalogRevision {
			return servers.ErrStaleRevision
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE server_catalogs SET durable_state = ?, issue_count = ? WHERE server_id = ?`, state, issueCount, fence.ServerID); err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, `DELETE FROM server_catalog_issues WHERE server_id = ?`, fence.ServerID); err != nil {
			return err
		}
		if issueCount > 0 {
			if _, err := transaction.ExecContext(ctx, `INSERT INTO server_catalog_issues (server_id, catalog_revision, issue_class, occurrences) VALUES (?, ?, 'descriptor_invalid', ?)`, fence.ServerID, currentRevision, issueCount); err != nil {
				return err
			}
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE gateway_meta SET revision = revision + 1 WHERE singleton = 1`); err != nil {
			return err
		}
		status, err = statusTx(ctx, transaction, fence.ServerID)
		return err
	})
	return status, mapStoreError(err)
}

func (repository *Repository) IdentityStatus(ctx context.Context) (contract.LimitStatus, error) {
	var count int64
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		return transaction.QueryRowContext(ctx, `SELECT count(*) FROM durable_tool_identities`).Scan(&count)
	})
	limit := fixedLimit("durable_tool_identities")
	return contract.LimitStatus{InUse: count, Limit: limit, Saturated: count >= limit}, mapStoreError(err)
}

func (repository *Repository) Status(ctx context.Context, serverID string) (DurableStatus, error) {
	var status DurableStatus
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		var err error
		status, err = statusTx(ctx, transaction, serverID)
		return err
	})
	return status, mapStoreError(err)
}

func (repository *Repository) GetDescriptor(ctx context.Context, serverID, toolID string) (contract.ToolDescriptor, error) {
	var resource contract.ToolDescriptor
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		return scanDescriptor(transaction.QueryRowContext(ctx, descriptorSelect+`
			WHERE identity.server_id = ? AND identity.id = ?`, serverID, toolID), nil, &resource)
	})
	return resource, mapStoreError(err)
}

func (repository *Repository) ListDescriptors(ctx context.Context, serverID string, retired contract.DescriptorRetiredFilter, cursor *DescriptorCursor, limit int) (DescriptorPage, error) {
	if _, err := contract.ParseDescriptorRetiredFilter(string(retired)); err != nil || limit < 1 || int64(limit) > fixedLimit("s2_list_page") {
		return DescriptorPage{}, servers.ErrInvalidInput
	}
	var page DescriptorPage
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		status, err := statusTx(ctx, transaction, serverID)
		if err != nil {
			return err
		}
		revision := "0"
		if status.Revision != nil {
			revision = *status.Revision
		}
		upper, after, afterID := int64(0), int64(0), ""
		if cursor == nil {
			if err := transaction.QueryRowContext(ctx, `
				SELECT coalesce(max(insertion_sequence), 0)
				FROM durable_tool_identities WHERE server_id = ?`, serverID).Scan(&upper); err != nil {
				return err
			}
		} else {
			if cursor.ServerID != serverID || cursor.Retired != retired || cursor.CatalogRevision != revision || cursor.Upper < 0 || cursor.After < 0 || cursor.After > cursor.Upper {
				return servers.ErrStaleCursor
			}
			upper, after, afterID = cursor.Upper, cursor.After, cursor.AfterID
		}
		predicate := ""
		switch retired {
		case contract.DescriptorRetiredExclude:
			predicate = " AND descriptor.retired_at IS NULL"
		case contract.DescriptorRetiredOnly:
			predicate = " AND descriptor.retired_at IS NOT NULL"
		}
		rows, err := transaction.QueryContext(ctx, descriptorSelect+`
			WHERE identity.server_id = ? AND identity.insertion_sequence <= ?
			  AND (identity.insertion_sequence > ? OR (identity.insertion_sequence = ? AND identity.id > ?))`+predicate+`
			ORDER BY identity.insertion_sequence, identity.id LIMIT ?`, serverID, upper, after, after, afterID, limit+1)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		items := make([]DescriptorRecord, 0, limit+1)
		for rows.Next() {
			var sequence int64
			var resource contract.ToolDescriptor
			if err := scanDescriptor(rows, &sequence, &resource); err != nil {
				return err
			}
			items = append(items, DescriptorRecord{InsertionSequence: sequence, Resource: resource})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(items) > limit {
			last := items[limit-1]
			page.Next = &DescriptorCursor{ServerID: serverID, Retired: retired, CatalogRevision: revision, Upper: upper, After: last.InsertionSequence, AfterID: last.Resource.ID}
			items = items[:limit]
		}
		page.Items = items
		return nil
	})
	return page, mapStoreError(err)
}

func validateCommitFence(ctx context.Context, transaction *sql.Tx, fence CommitFence) error {
	var desiredRevision int64
	var desiredState contract.DesiredServerState
	if err := transaction.QueryRowContext(ctx, `SELECT desired_revision, desired_state FROM servers WHERE id = ?`, fence.ServerID).Scan(&desiredRevision, &desiredState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return servers.ErrNotFound
		}
		return err
	}
	if strconv.FormatInt(desiredRevision, 10) != fence.ExpectedDesiredRevision || desiredState != contract.DesiredServerEnabled {
		return servers.ErrStaleRevision
	}
	var registration int64
	if err := transaction.QueryRowContext(ctx, `SELECT revision FROM server_oauth_registrations WHERE server_id = ?`, fence.ServerID).Scan(&registration); err != nil {
		return err
	}
	if strconv.FormatInt(registration, 10) != fence.ExpectedRegistrationRevision {
		return servers.ErrStaleRevision
	}
	rows, err := transaction.QueryContext(ctx, `SELECT kind, revision FROM server_credentials WHERE server_id = ?`, fence.ServerID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	current := contract.CredentialRevisions{}
	for rows.Next() {
		var kind contract.ServerCredentialKind
		var revision int64
		if err := rows.Scan(&kind, &revision); err != nil {
			return err
		}
		switch kind {
		case contract.ServerCredentialStatic:
			current.StaticCredential = strconv.FormatInt(revision, 10)
		case contract.ServerCredentialOAuthClient:
			current.OAuthClient = strconv.FormatInt(revision, 10)
		case contract.ServerCredentialOAuthTokens:
			current.OAuthTokens = strconv.FormatInt(revision, 10)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if current != fence.ExpectedCredentialRevisions {
		return servers.ErrStaleRevision
	}
	return nil
}

func statusTx(ctx context.Context, transaction *sql.Tx, serverID string) (DurableStatus, error) {
	var state contract.DurableCatalogState
	var revision, toolCount, issueCount int64
	var lastSuccess sql.NullString
	err := transaction.QueryRowContext(ctx, `
		SELECT durable_state, durable_revision, durable_tool_count, issue_count, last_success_at
		FROM server_catalogs WHERE server_id = ?`, serverID).Scan(&state, &revision, &toolCount, &issueCount, &lastSuccess)
	if errors.Is(err, sql.ErrNoRows) {
		return DurableStatus{}, servers.ErrNotFound
	}
	if err != nil {
		return DurableStatus{}, err
	}
	status := DurableStatus{State: state, ToolCount: toolCount, IssueCount: issueCount}
	if revision != 0 {
		value := strconv.FormatInt(revision, 10)
		status.Revision = &value
	}
	if lastSuccess.Valid {
		value := lastSuccess.String
		status.LastSuccessAt = &value
	}
	return status, nil
}

func missingIdentityNames(ctx context.Context, transaction *sql.Tx, serverID string, tools []NormalizedTool) ([]string, error) {
	missing := make([]string, 0)
	for _, tool := range tools {
		var count int
		if err := transaction.QueryRowContext(ctx, `
			SELECT count(*) FROM durable_tool_identities
			WHERE server_id = ? AND upstream_name = ?`, serverID, tool.Key.UpstreamName).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			missing = append(missing, tool.Key.UpstreamName)
		}
	}
	return missing, nil
}

func checkIdentityCapacity(ctx context.Context, transaction *sql.Tx, serverID string, additions int64) error {
	var perServer, global int64
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM durable_tool_identities WHERE server_id = ?`, serverID).Scan(&perServer); err != nil {
		return err
	}
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM durable_tool_identities`).Scan(&global); err != nil {
		return err
	}
	if perServer+additions > fixedLimit("durable_tool_identities_per_server") || global+additions > fixedLimit("durable_tool_identities") {
		return servers.ErrResourceLimit
	}
	return nil
}

func toolByName(tools []NormalizedTool, name string) NormalizedTool {
	for _, tool := range tools {
		if tool.Key.UpstreamName == name {
			return tool
		}
	}
	return NormalizedTool{}
}

const descriptorSelect = `
	SELECT identity.insertion_sequence, identity.id, identity.server_id,
	       identity.upstream_name, identity.external_name,
	       descriptor.descriptor_json, descriptor.fingerprint,
	       descriptor.catalog_revision, identity.first_seen_at,
	       descriptor.last_seen_at, descriptor.retired_at
	FROM durable_tool_identities AS identity
	JOIN tool_descriptors AS descriptor ON descriptor.tool_id = identity.id`

type rowScanner interface {
	Scan(...any) error
}

func scanDescriptor(row rowScanner, sequence *int64, resource *contract.ToolDescriptor) error {
	var insertion int64
	var descriptorJSON string
	var retired sql.NullString
	if err := row.Scan(&insertion, &resource.ID, &resource.ServerID, &resource.UpstreamName, &resource.ExternalName, &descriptorJSON, &resource.Fingerprint, &resource.CatalogRevision, &resource.FirstSeenAt, &resource.LastSeenAt, &retired); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return servers.ErrNotFound
		}
		return err
	}
	if json.Unmarshal([]byte(descriptorJSON), &resource.Descriptor) != nil {
		return servers.ErrStorageUnavailable
	}
	if retired.Valid {
		value := retired.String
		resource.RetiredAt = &value
	}
	if sequence != nil {
		*sequence = insertion
	}
	return nil
}

func mapStoreError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, storage.ErrStorageLatched) {
		return servers.ErrStorageUnavailable
	}
	return err
}
