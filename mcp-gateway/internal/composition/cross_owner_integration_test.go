//go:build integration

package composition

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/grantrequests"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/mcpingress"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateVsTargetAndPolicyIntegration(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	options, cleanup := newCompositionOptionsWithFault(t, func(point storage.FaultPoint) error {
		if point == storage.FaultArmCreate && armed.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return nil
	})
	defer cleanup()
	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	principal := integrationPrincipal(t, built, "creation owner")
	server := createCompositionServer(t, built.servers, "creation-race", false, "/bin/true")
	policy := contract.Policy{Scope: contract.PolicyServer, Target: server.Namespace, FutureToolsAcknowledged: true}

	armed.Store(true)
	created := make(chan contract.CreateGrantRequestResult, 1)
	createErr := make(chan error, 1)
	go func() {
		result, createError := built.requests.CreateOrExisting(context.Background(), grantrequests.CreateRequest{PrincipalID: principal.Principal.ID, Policy: policy})
		created <- result
		createErr <- createError
	}()
	<-entered
	_, err = built.servers.Delete(context.Background(), server.ID, server.DesiredRevision)
	assert.Error(t, err, "target mutation must not pass an in-flight request transaction")
	close(release)
	require.NoError(t, <-createErr)
	assert.Equal(t, contract.RequestCreated, (<-created).Outcome)

	deleted, err := built.servers.Delete(context.Background(), server.ID, server.DesiredRevision)
	require.NoError(t, err)
	duration := "60"
	result, err := built.requests.CreateOrExisting(context.Background(), grantrequests.CreateRequest{PrincipalID: principal.Principal.ID, Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: server.Namespace, DurationSeconds: &duration, FutureToolsAcknowledged: true,
	}})
	require.NoError(t, err)
	assert.Equal(t, contract.RequestTargetUnavailable, result.Outcome)
	assert.Equal(t, contract.DesiredServerDeleted, deleted.Server.DesiredState)

	policyServer := createCompositionServer(t, built.servers, "policy-first", false, "/bin/true")
	_, err = built.authorization.CreateGrant(context.Background(), authorization.CreateGrantRequest{
		PrincipalID: principal.Principal.ID, Effect: contract.GrantDeny, ServerID: policyServer.ID,
	}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
	require.NoError(t, err)
	result, err = built.requests.CreateOrExisting(context.Background(), grantrequests.CreateRequest{PrincipalID: principal.Principal.ID, Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: policyServer.Namespace, FutureToolsAcknowledged: true,
	}})
	require.NoError(t, err)
	assert.Equal(t, contract.RequestDenyConflict, result.Outcome)
}

func TestApprovalVsPolicyAndCapacityIntegration(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	options, cleanup := newCompositionOptionsWithFault(t, func(point storage.FaultPoint) error {
		if point == storage.FaultArmCreate && armed.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return nil
	})
	defer cleanup()
	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	principal := integrationPrincipal(t, built, "approval owner")
	server := createCompositionServer(t, built.servers, "approval-race", false, "/bin/true")
	policy := contract.Policy{Scope: contract.PolicyServer, Target: server.Namespace, FutureToolsAcknowledged: true}
	created, err := built.requests.CreateOrExisting(context.Background(), grantrequests.CreateRequest{PrincipalID: principal.Principal.ID, Policy: policy})
	require.NoError(t, err)
	before, err := built.authorization.AuthorizationRevision(context.Background())
	require.NoError(t, err)

	armed.Store(true)
	approved := make(chan grantrequests.ApproveResult, 1)
	approveErr := make(chan error, 1)
	go func() {
		result, approveError := built.requests.Approve(context.Background(), built.authorization, grantrequests.ApproveRequest{ID: created.Request.ID, ExpectedRevision: "1", ApprovedPolicy: policy})
		approved <- result
		approveErr <- approveError
	}()
	<-entered
	_, err = built.authorization.CreateGrant(context.Background(), authorization.CreateGrantRequest{
		PrincipalID: principal.Principal.ID, Effect: contract.GrantDeny, ServerID: server.ID,
	}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
	assert.Error(t, err, "policy/capacity admission must not cross an in-flight approval gate")
	close(release)
	require.NoError(t, <-approveErr)
	result := <-approved
	assert.Equal(t, contract.RequestApproved, result.Request.State)
	after, err := built.authorization.AuthorizationRevision(context.Background())
	require.NoError(t, err)
	beforeValue, err := strconv.ParseInt(before, 10, 64)
	require.NoError(t, err)
	afterValue, err := strconv.ParseInt(after, 10, 64)
	require.NoError(t, err)
	assert.Equal(t, beforeValue+1, afterValue)

	secondServer := createCompositionServer(t, built.servers, "approval-policy-first", false, "/bin/true")
	secondPolicy := contract.Policy{Scope: contract.PolicyServer, Target: secondServer.Namespace, FutureToolsAcknowledged: true}
	second, err := built.requests.CreateOrExisting(context.Background(), grantrequests.CreateRequest{PrincipalID: principal.Principal.ID, Policy: secondPolicy})
	require.NoError(t, err)
	_, err = built.authorization.CreateGrant(context.Background(), authorization.CreateGrantRequest{
		PrincipalID: principal.Principal.ID, Effect: contract.GrantDeny, ServerID: secondServer.ID,
	}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
	require.NoError(t, err)
	_, err = built.requests.Approve(context.Background(), built.authorization, grantrequests.ApproveRequest{ID: second.Request.ID, ExpectedRevision: "1", ApprovedPolicy: secondPolicy})
	assert.ErrorIs(t, err, grantrequests.ErrConflict)
	pending, found, err := built.requests.GetOwned(context.Background(), principal.Principal.ID, second.Request.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, contract.RequestPending, pending.State)

	capacityServer := createCompositionServer(t, built.servers, "approval-capacity-first", false, "/bin/true")
	capacityPolicy := contract.Policy{Scope: contract.PolicyServer, Target: capacityServer.Namespace, FutureToolsAcknowledged: true}
	capacityRequest, err := built.requests.CreateOrExisting(context.Background(), grantrequests.CreateRequest{PrincipalID: principal.Principal.ID, Policy: capacityPolicy})
	require.NoError(t, err)
	grantLimit, found := contract.FixedLimitByName("grants")
	require.True(t, found)
	require.NoError(t, options.Store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		var current int64
		if queryErr := transaction.QueryRow(`SELECT count(*) FROM grants`).Scan(&current); queryErr != nil {
			return queryErr
		}
		_, insertErr := transaction.Exec(`WITH RECURSIVE sequence(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM sequence WHERE n < ?)
			INSERT INTO grants (id, principal_id, effect, server_id, created_at)
			SELECT printf('%026d', n), ?, 'allow', ?, '2026-08-27T00:00:00.000000000Z' FROM sequence`, grantLimit.Maximum-current, principal.Principal.ID, capacityServer.ID)
		return insertErr
	}))
	_, grants, err := built.AuthorizationOccupancy(context.Background())
	require.NoError(t, err)
	assert.Equal(t, grantLimit.Maximum, grants.InUse)
	assert.True(t, grants.Saturated)
	_, err = built.requests.Approve(context.Background(), built.authorization, grantrequests.ApproveRequest{ID: capacityRequest.Request.ID, ExpectedRevision: "1", ApprovedPolicy: capacityPolicy})
	assert.ErrorIs(t, err, grantrequests.ErrResourceLimit)
	_, grantsAfter, occupancyErr := built.AuthorizationOccupancy(context.Background())
	require.NoError(t, occupancyErr)
	assert.Equal(t, grants, grantsAfter, "capacity failure must not leave a partial grant")
	pending, found, err = built.requests.GetOwned(context.Background(), principal.Principal.ID, capacityRequest.Request.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, contract.RequestPending, pending.State)
}

func TestCredentialAdmissionOrderingIntegration(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	principal := integrationPrincipal(t, built, "admission owner")
	credential, err := built.authorization.IssueCredential(context.Background(), principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	lease, err := built.authorization.Authenticate(context.Background(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()

	synthetic, found := catalog.ResolveSyntheticCall("mcp_gateway.get_identity")
	require.True(t, found)
	executions := 0
	target, err := invocation.NewLocalTarget(synthetic, func(ctx context.Context, subject authorization.AdmittedSubject, _ strictjson.Value) invocation.LocalCallResult {
		executions++
		count, countErr := built.invocationRepository.Count(ctx)
		require.NoError(t, countErr)
		assert.Equal(t, int64(1), count, "audit admission must commit before local mutation")
		_, revokeErr := built.authorization.RevokeCredential(ctx, subject.PrincipalID(), subject.PrincipalRevision())
		require.NoError(t, revokeErr, "detached admission must release the authority gate before the handler")
		return invocation.LocalSuccess(json.RawMessage(`{"content":[],"structuredContent":{"ok":true}}`))
	})
	require.NoError(t, err)
	service, err := invocation.NewServiceWithLocal(built.invocationRepository, built.authorization, built.activeCatalog.Routes(), func(name string) (invocation.LocalTarget, bool) {
		return target, name == "mcp_gateway.get_identity"
	})
	require.NoError(t, err)
	adapter := &invocationCallAdapter{service: service, pipelines: built.invocationPipelines}
	response := adapter.Call(context.Background(), lease, mcpingress.ToolsCallRequest{Params: integrationCallParams("mcp_gateway.get_identity"), WireValid: true})
	require.NotNil(t, response.Result)
	assert.Equal(t, 1, executions)
	_, err = built.authorization.Authenticate(context.Background(), credential.Bearer)
	assert.Error(t, err)

	var fingerprint, redacted, terminal string
	require.NoError(t, options.Store.View(context.Background(), func(transaction *sql.Tx) error {
		return transaction.QueryRow(`SELECT descriptor_fingerprint, redacted_arguments, terminal_class FROM invocations`).Scan(&fingerprint, &redacted, &terminal)
	}))
	assert.NotEmpty(t, fingerprint)
	assert.JSONEq(t, `{}`, redacted)
	assert.Equal(t, string(contract.TerminalSucceeded), terminal)
}

func TestCatalogEvidenceReplacementIntegration(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	options, cleanup := newCompositionOptionsWithFault(t, func(point storage.FaultPoint) error {
		if point == storage.FaultArmCreate && armed.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return nil
	})
	defer cleanup()
	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	principal := integrationPrincipal(t, built, "evidence owner")
	server := createCompositionServer(t, built.servers, "evidence", true, "/bin/true")
	first := integrationCatalogCandidate(t, server.ID, server.Namespace, `{"type":"object","properties":{"value":{"type":"string"}}}`)
	_, err = built.catalogRepository.Commit(context.Background(), integrationCatalogFence(server, "0"), first)
	require.NoError(t, err)
	second := integrationCatalogCandidate(t, server.ID, server.Namespace, `{"type":"object","properties":{"value":{"type":"number"}}}`)
	armed.Store(true)
	createdResult := make(chan contract.CreateGrantRequestResult, 1)
	createErr := make(chan error, 1)
	go func() {
		created, requestErr := built.requests.CreateOrExisting(context.Background(), grantrequests.CreateRequest{PrincipalID: principal.Principal.ID, Policy: contract.Policy{
			Scope: contract.PolicyTool, Target: server.Namespace + ".echo",
		}})
		createdResult <- created
		createErr <- requestErr
	}()
	<-entered
	_, err = built.catalogRepository.Commit(context.Background(), integrationCatalogFence(server, "1"), second)
	assert.Error(t, err, "catalog replacement must not cross an in-flight evidence transaction")
	close(release)
	require.NoError(t, <-createErr)
	created := <-createdResult
	require.NotNil(t, created.Request)
	before, err := built.requests.GetAdmin(context.Background(), created.Request.ID)
	require.NoError(t, err)
	require.NotNil(t, before.SubmittedEvidence)
	originalFingerprint := before.SubmittedEvidence.Fingerprint

	_, err = built.catalogRepository.Commit(context.Background(), integrationCatalogFence(server, "1"), second)
	require.NoError(t, err)
	after, err := built.requests.GetAdmin(context.Background(), created.Request.ID)
	require.NoError(t, err)
	require.NotNil(t, after.SubmittedEvidence)
	assert.Equal(t, originalFingerprint, after.SubmittedEvidence.Fingerprint, "submitted evidence must remain immutable")
	require.NotNil(t, after.CurrentTarget.Fingerprint)
	assert.NotEqual(t, originalFingerprint, *after.CurrentTarget.Fingerprint, "current comparison must reflect replacement")
}

func integrationPrincipal(t *testing.T, built *Composition, name string) contract.PrincipalCreation {
	t.Helper()
	principal, err := built.authorization.CreatePrincipal(context.Background(), authorization.CreatePrincipalRequest{DisplayName: name, Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	return principal
}

func integrationCatalogFence(server servers.Server, revision string) catalog.CommitFence {
	return catalog.CommitFence{
		ServerID: server.ID, ExpectedDesiredRevision: server.DesiredRevision,
		ExpectedRegistrationRevision: "0", ExpectedCredentialRevisions: contract.CredentialRevisions{
			StaticCredential: "0", OAuthClient: "0", OAuthTokens: "0",
		},
		ExpectedCatalogRevision: revision,
	}
}

func integrationCatalogCandidate(t *testing.T, serverID, namespace, inputSchema string) catalog.NormalizedCandidate {
	t.Helper()
	normalized, err := catalog.NormalizeTool(catalog.RawTool{
		UpstreamName: "echo", ExternalName: namespace + ".echo",
		Descriptor: json.RawMessage(`{"name":"echo","inputSchema":` + inputSchema + `}`),
	}, catalog.NormalizeOptions{ServerID: serverID})
	require.NoError(t, err)
	return catalog.NormalizedCandidate{Tools: []catalog.NormalizedTool{normalized}, RawCount: 1, Pages: 1}
}

func integrationCallParams(name string) strictjson.Value {
	return strictjson.Value{Type: strictjson.ValueObject, Object: []strictjson.Member{
		{Name: "name", Value: strictjson.Value{Type: strictjson.ValueString, String: name}},
		{Name: "arguments", Value: strictjson.Value{Type: strictjson.ValueObject}},
	}}
}
