//go:build e2e

package e2e

import (
	"encoding/json"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestE2EReadOnlyGrantRequestApprovalRestartAndRefresh(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer func() {
		if harness.process != nil {
			harness.Stop(syscall.SIGTERM)
		}
	}()
	tool := func(name, annotations string) fixtureTool {
		return fixtureTool{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`), Annotations: json.RawMessage(annotations)}
	}
	catalog := harness.SetupCurrentCatalog("readonly", []fixtureTool{tool("read", `{"readOnlyHint":true}`), tool("write", `{"readOnlyHint":false}`), tool("missing", ``), tool("null", `{"readOnlyHint":null}`)})
	principal := harness.CreatePrincipal("Read-only agent", contract.VisibilityAllowedOnly)
	issued := harness.IssueCredential(principal)
	policy := contract.Policy{Scope: contract.PolicyServer, Target: catalog.Namespace, FutureToolsAcknowledged: true, ReadOnly: true}
	createID := json.RawMessage(`"request"`)
	response := harness.ModernSelfServiceCall(issued.Bearer, createID, "create_grant_request", contract.CreateGrantRequestInput{Policy: policy})
	created := decodeSelfServiceResult[contract.CreateGrantRequestResult](t, response, createID, contract.SummaryGrantRequestProcessed)
	require.NotNil(t, created.Request)
	require.True(t, created.Request.RequestedPolicy.ReadOnly)
	harness.Restart()
	waitForStdioServer(t, harness, catalog.ServerID, func(server stdioServerView) bool {
		return activeCatalog(server) && server.Runtime.Reconciliation.InUse == 0
	})
	pending := harness.GetGrantRequest(created.Request.ID)
	require.True(t, pending.Resource.RequestedPolicy.ReadOnly)
	approved := harness.ApproveGrantRequest(pending, policy)
	require.True(t, approved.Resource.ApprovedPolicy.ReadOnly)
	require.True(t, harness.GetGrant(*approved.Resource.ApprovedGrantID).ReadOnly)
	harness.Restart()
	waitForStdioServer(t, harness, catalog.ServerID, func(server stdioServerView) bool {
		return activeCatalog(server) && server.Runtime.Reconciliation.InUse == 0
	})
	require.True(t, harness.GetGrant(*approved.Resource.ApprovedGrantID).ReadOnly)
	require.True(t, harness.GetGrantRequest(created.Request.ID).Resource.ApprovedPolicy.ReadOnly)
	catalog.Fixture.SetCallOutcome(fixtureCallSuccess)
	for _, name := range []string{"write", "missing", "null"} {
		callID := json.RawMessage(`"blocked"`)
		blocked := harness.ModernCall(issued.Bearer, callID, "readonly."+name, json.RawMessage(`{}`))
		assertCallRejected(t, blocked, callID, contract.RejectionBlock, false)
	}
	require.Zero(t, catalog.CallCount())
	called := harness.ModernCall(issued.Bearer, json.RawMessage(`"read"`), "readonly.read", json.RawMessage(`{}`))
	require.Contains(t, string(called.Body), `"result"`)
	require.Equal(t, 1, catalog.CallCount())
	listed := harness.ModernList(issued.Bearer, json.RawMessage(`"list"`), "")
	require.Contains(t, string(listed.Body), `"readonly.read"`)
	require.NotContains(t, string(listed.Body), `"readonly.write"`)
	grantsID := json.RawMessage(`"grants"`)
	grants := decodeSelfServiceResult[contract.ListGrantsResult](t, harness.ModernSelfServiceCall(issued.Bearer, grantsID, "list_grants", contract.ListGrantsInput{}), grantsID, contract.SummaryGrantsReturned)
	found := false
	for _, grant := range grants.Items {
		if grant.ID == *approved.Resource.ApprovedGrantID {
			require.True(t, grant.Policy.ReadOnly)
			found = true
		}
	}
	require.True(t, found)
	catalog.Fixture.SetTools([]fixtureTool{tool("read", `{"readOnlyHint":false}`), tool("future", `{"readOnlyHint":true}`)})
	refresh := createServerOperation(t, harness, catalog.ServerID, catalog.ETag, string(contract.OperationRefreshCatalog), "readonly-refresh")
	harness.WaitOperation(catalog.ServerID, refresh.ID, contract.OperationSucceeded)
	harness.WaitSettledOperation(catalog.ServerID, refresh.ID)
	blockedID := json.RawMessage(`"changed"`)
	assertCallRejected(t, harness.ModernCall(issued.Bearer, blockedID, "readonly.read", json.RawMessage(`{}`)), blockedID, contract.RejectionBlock, false)
	require.Equal(t, 1, catalog.CallCount())
	future := harness.ModernCall(issued.Bearer, json.RawMessage(`"future"`), "readonly.future", json.RawMessage(`{}`))
	require.Contains(t, string(future.Body), `"result"`)
	require.Equal(t, 2, catalog.CallCount())
	// Exercise administrator grant creation independently of approval.
	direct := harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: catalog.ServerID, ReadOnly: true})
	require.True(t, direct.ReadOnly)
	require.True(t, harness.GetGrant(direct.ID).ReadOnly)
	defaults := harness.ListGrants(principal.Resource.ID, contract.SyntheticServerID)
	for _, grant := range defaults {
		harness.DeleteGrant(grant.ID)
	}
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: contract.SyntheticServerID, ReadOnly: true})
	identityID := json.RawMessage(`"identity"`)
	identity := decodeSelfServiceResult[contract.GetIdentityResult](t, harness.ModernSelfServiceCall(issued.Bearer, identityID, "get_identity", struct{}{}), identityID, contract.SummaryIdentityReturned)
	require.Equal(t, principal.Resource.ID, identity.Identity.ID)
	blockedCreateID := json.RawMessage(`"blocked-create"`)
	assertCallRejected(t, harness.ModernSelfServiceCall(issued.Bearer, blockedCreateID, "create_grant_request", contract.CreateGrantRequestInput{Policy: policy}), blockedCreateID, contract.RejectionBlock, true)
}
