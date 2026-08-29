//go:build integration

package grantrequests

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestRetentionAndEvidenceLimitsIntegration(t *testing.T) {
	t.Run("row capacity evicts oldest terminal only", func(t *testing.T) {
		namespaces := &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
			"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
		}}
		repository, store := newRequestRepository(t, requestRepositoryOptions{
			clock: &countingRequestClock{now: requestTestTime}, namespaces: namespaces,
			descriptors: &fakeDescriptorInspector{}, denies: new(fakeDenyInspector), invalidate: func(contract.Invalidation) {},
		})
		require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			inserter, err := newFixtureRequestInserter(context.Background(), transaction)
			if err != nil {
				return err
			}
			defer inserter.Close()
			for sequence := int64(1); sequence <= fixedLimit("grant_requests"); sequence++ {
				if err := inserter.Insert(context.Background(), fixtureID(sequence), fixtureID(100000), contract.RequestCancelled, []byte(fmt.Sprintf("row-%d", sequence)), nil); err != nil {
					return err
				}
			}
			return nil
		}))
		result, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: fixtureID(200000), Policy: contract.Policy{
			Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
		}})
		require.NoError(t, err)
		assert.Equal(t, contract.RequestCreated, result.Outcome)
		require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
			var rows, oldest int64
			require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT count(*) FROM grant_requests`).Scan(&rows))
			require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT count(*) FROM grant_requests WHERE id = ?`, fixtureID(1)).Scan(&oldest))
			assert.Equal(t, fixedLimit("grant_requests"), rows)
			assert.Zero(t, oldest)
			return nil
		}))

		var sequenceBefore int64
		require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
			return transaction.QueryRowContext(context.Background(), `SELECT seq FROM sqlite_sequence WHERE name = 'grant_requests'`).Scan(&sequenceBefore)
		}))
		collisionID, err := admin.NewID(requestTestTime, bytes.NewReader(bytes.Repeat([]byte{0x22}, 10)))
		require.NoError(t, err)
		require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			_, insertErr := transaction.ExecContext(context.Background(), `INSERT INTO grant_request_identities (id, created_at) VALUES (?, ?)`, collisionID, requestTimestamp(requestTestTime))
			return insertErr
		}))
		duration := "60"
		_, err = repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: fixtureID(200001), Policy: contract.Policy{
			Scope: contract.PolicyServer, Target: "sample", DurationSeconds: &duration, FutureToolsAcknowledged: true,
		}})
		require.ErrorIs(t, err, ErrIdentityUnavailable)
		require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
			var rows, retained, sequenceAfter int64
			require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT count(*) FROM grant_requests`).Scan(&rows))
			require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT seq FROM sqlite_sequence WHERE name = 'grant_requests'`).Scan(&sequenceAfter))
			require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT count(*) FROM grant_requests WHERE id = ?`, fixtureID(2)).Scan(&retained))
			assert.Equal(t, fixedLimit("grant_requests"), rows)
			assert.Equal(t, int64(1), retained, "identity collision must roll back provisional eviction")
			assert.Equal(t, sequenceBefore, sequenceAfter, "failed insertion must roll back AUTOINCREMENT state")
			return nil
		}))
	})

	t.Run("row capacity of pending requests rejects without mutation", func(t *testing.T) {
		namespaces := &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
			"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
		}}
		repository, store := newRequestRepository(t, requestRepositoryOptions{
			clock: &countingRequestClock{now: requestTestTime}, namespaces: namespaces,
			descriptors: &fakeDescriptorInspector{}, denies: new(fakeDenyInspector), invalidate: func(contract.Invalidation) {},
		})
		require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			inserter, err := newFixtureRequestInserter(context.Background(), transaction)
			if err != nil {
				return err
			}
			defer inserter.Close()
			for sequence := int64(1); sequence <= fixedLimit("grant_requests"); sequence++ {
				principal := fixtureID(250000 + sequence/127)
				if err := inserter.Insert(context.Background(), fixtureID(sequence), principal, contract.RequestPending, []byte(fmt.Sprintf("full-%d", sequence)), nil); err != nil {
					return err
				}
			}
			return nil
		}))
		result, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: fixtureID(299999), Policy: contract.Policy{
			Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
		}})
		require.NoError(t, err)
		assert.Equal(t, contract.RequestLimitReached, result.Outcome)
		require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
			var rows int64
			require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT count(*) FROM grant_requests`).Scan(&rows))
			assert.Equal(t, fixedLimit("grant_requests"), rows)
			return nil
		}))
	})

	t.Run("pending capacity rejects without eviction", func(t *testing.T) {
		namespaces := &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
			"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
		}}
		repository, store := newRequestRepository(t, requestRepositoryOptions{
			clock: &countingRequestClock{now: requestTestTime}, namespaces: namespaces,
			descriptors: &fakeDescriptorInspector{}, denies: new(fakeDenyInspector), invalidate: func(contract.Invalidation) {},
		})
		principalID := fixtureID(300000)
		require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			inserter, err := newFixtureRequestInserter(context.Background(), transaction)
			if err != nil {
				return err
			}
			defer inserter.Close()
			for sequence := int64(1); sequence <= fixedLimit("pending_grant_requests_per_principal"); sequence++ {
				if err := inserter.Insert(context.Background(), fixtureID(sequence), principalID, contract.RequestPending, []byte(fmt.Sprintf("pending-%d", sequence)), nil); err != nil {
					return err
				}
			}
			return nil
		}))
		result, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: principalID, Policy: contract.Policy{
			Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
		}})
		require.NoError(t, err)
		assert.Equal(t, contract.RequestLimitReached, result.Outcome)
	})

	t.Run("exact evidence byte boundary rejects then evicts terminal prefix", func(t *testing.T) {
		large := fixtureEvidence(t, 90000)
		minimum := fixtureEvidence(t, 0)
		limit := fixedLimit("grant_request_evidence_bytes")
		largeCount := (limit - int64(len(minimum))) / int64(len(large))
		remaining := limit - largeCount*int64(len(large))
		if remaining < int64(len(minimum)) {
			largeCount--
			remaining += int64(len(large))
		}
		final := fixtureEvidence(t, int(remaining)-len(minimum))
		require.Equal(t, remaining, int64(len(final)))

		namespaces := &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
			"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
		}}
		descriptors := &fakeDescriptorInspector{descriptors: map[string]catalog.DurableDescriptor{
			"sample.echo": requestDescriptor(t, requestID(400), requestID(401), "sample", "echo", contract.EvidenceCurrent),
		}}
		repository, store := newRequestRepository(t, requestRepositoryOptions{
			clock: &countingRequestClock{now: requestTestTime}, namespaces: namespaces,
			descriptors: descriptors, denies: new(fakeDenyInspector), invalidate: func(contract.Invalidation) {},
		})
		require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			inserter, err := newFixtureRequestInserter(context.Background(), transaction)
			if err != nil {
				return err
			}
			defer inserter.Close()
			for sequence := int64(1); sequence <= largeCount; sequence++ {
				principal := fixtureID(400000 + sequence/127)
				if err := inserter.Insert(context.Background(), fixtureID(sequence), principal, contract.RequestPending, []byte(fmt.Sprintf("evidence-%d", sequence)), large); err != nil {
					return err
				}
			}
			return inserter.Insert(context.Background(), fixtureID(largeCount+1), fixtureID(500000), contract.RequestPending, []byte("evidence-final"), final)
		}))
		principalID := fixtureID(600000)
		request := CreateRequest{PrincipalID: principalID, Policy: contract.Policy{Scope: contract.PolicyTool, Target: "sample.echo"}}
		result, err := repository.CreateOrExisting(context.Background(), request)
		require.NoError(t, err)
		assert.Equal(t, contract.RequestLimitReached, result.Outcome)

		firstID := fixtureID(1)
		require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			closed := requestTimestamp(requestTestTime.Add(time.Second))
			_, transitionErr := transaction.ExecContext(context.Background(), `UPDATE grant_requests SET state = 'cancelled', revision = 2, updated_at = ?, closed_at = ? WHERE id = ?`, closed, closed, firstID)
			return transitionErr
		}))
		result, err = repository.CreateOrExisting(context.Background(), request)
		require.NoError(t, err)
		assert.Equal(t, contract.RequestCreated, result.Outcome)
		require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
			var total, calculated, first int64
			require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT total_bytes FROM grant_request_evidence_bytes WHERE singleton = 1`).Scan(&total))
			require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT COALESCE(sum(COALESCE(length(submitted_evidence), 0) + COALESCE(length(approved_evidence), 0)), 0) FROM grant_requests`).Scan(&calculated))
			require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT count(*) FROM grant_requests WHERE id = ?`, firstID).Scan(&first))
			assert.LessOrEqual(t, total, limit)
			assert.Equal(t, calculated, total)
			assert.Zero(t, first)
			return nil
		}))
	})
}

type fixtureRequestInserter struct {
	identity *sql.Stmt
	request  *sql.Stmt
}

func newFixtureRequestInserter(ctx context.Context, transaction *sql.Tx) (*fixtureRequestInserter, error) {
	identity, err := transaction.PrepareContext(ctx, `INSERT INTO grant_request_identities (id, created_at) VALUES (?, ?)`)
	if err != nil {
		return nil, err
	}
	request, err := transaction.PrepareContext(ctx, `INSERT INTO grant_requests (
		id, principal_id, state, revision, resolved_server_id, resolved_upstream_name,
		requested_scope, requested_target, requested_constraint, requested_duration_seconds,
		requested_future_tools_acknowledged, dedupe_version, dedupe_bytes, submitted_evidence,
		approved_scope, approved_target, approved_constraint, approved_duration_seconds,
		approved_future_tools_acknowledged, approved_grant_id, rejection_reason, approved_evidence,
		created_at, updated_at, closed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, 1, ?, ?,
		NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, ?, ?, ?)`)
	if err != nil {
		_ = identity.Close()
		return nil, err
	}
	return &fixtureRequestInserter{identity: identity, request: request}, nil
}

func (inserter *fixtureRequestInserter) Close() {
	_ = inserter.request.Close()
	_ = inserter.identity.Close()
}

func (inserter *fixtureRequestInserter) Insert(ctx context.Context, id, principalID string, state contract.GrantRequestState, dedupe, evidence []byte) error {
	revision := 1
	var closed any
	updated := requestTimestamp(requestTestTime)
	if state != contract.RequestPending {
		revision = 2
		closed = requestTimestamp(requestTestTime.Add(time.Second))
		updated = closed.(string)
	}
	if _, err := inserter.identity.ExecContext(ctx, id, requestTimestamp(requestTestTime)); err != nil {
		return err
	}
	var upstream any
	scope, target, acknowledged := contract.PolicyServer, "sample", true
	if len(evidence) != 0 {
		upstream, scope, target, acknowledged = "seed", contract.PolicyTool, "sample.seed", false
	}
	_, err := inserter.request.ExecContext(ctx, id, principalID, state, revision, requestID(400), upstream, scope, target, acknowledged,
		dedupe, nullableEvidence(evidence), requestTimestamp(requestTestTime), updated, closed)
	return err
}

func fixtureEvidence(t *testing.T, padding int) []byte {
	t.Helper()
	descriptor := json.RawMessage(`{"name":"seed","inputSchema":{"type":"object","description":"` + strings.Repeat("x", padding) + `"}}`)
	normalized, err := catalog.NormalizeTool(catalog.RawTool{
		UpstreamName: "seed", ExternalName: "sample.seed", Descriptor: descriptor,
	}, catalog.NormalizeOptions{ServerID: requestID(400), AllowHeaderBindings: true})
	require.NoError(t, err)
	source := catalog.DurableDescriptor{State: contract.EvidenceCurrent, Resource: contract.ToolDescriptor{
		ID: requestID(402), ServerID: requestID(400), UpstreamName: "seed", ExternalName: "sample.seed",
		Descriptor: normalized.Descriptor, Fingerprint: normalized.Fingerprint, CatalogRevision: "1",
		FirstSeenAt: requestTimestamp(requestTestTime), LastSeenAt: requestTimestamp(requestTestTime),
	}}
	_, evidence, err := BuildDescriptorEvidence(source, "sample", requestTestTime)
	require.NoError(t, err)
	return evidence
}

func fixtureID(value int64) string { return fmt.Sprintf("%026d", value) }
