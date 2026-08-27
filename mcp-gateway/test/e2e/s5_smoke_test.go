//go:build e2e

package e2e

import (
	"encoding/json"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS5E2ESmoke(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer func() {
		if harness.process != nil {
			harness.Stop(syscall.SIGTERM)
		}
	}()
	catalog := harness.SetupCurrentCatalog("s5-smoke", []fixtureTool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	principal := harness.CreatePrincipal("S5 smoke agent", contract.VisibilityRequestable)
	issued := harness.IssueCredential(principal)

	listID := json.RawMessage(`"smoke-list"`)
	listResponse := harness.ModernList(issued.Bearer, listID, "")
	assert.Equal(t, []string{
		"mcp_gateway.cancel_grant_request", "mcp_gateway.create_grant_request", "mcp_gateway.get_grant_request",
		"mcp_gateway.get_identity", "mcp_gateway.list_grant_requests", "mcp_gateway.list_grants", "s5-smoke.echo",
	}, discoveryToolNames(t, listResponse))

	policy := contract.Policy{Scope: contract.PolicyTool, Target: "s5-smoke.echo"}
	createID := json.RawMessage(`"smoke-request"`)
	createResponse := harness.ModernSelfServiceCall(issued.Bearer, createID, "create_grant_request", contract.CreateGrantRequestInput{Policy: policy})
	created := decodeSelfServiceResult[contract.CreateGrantRequestResult](t, createResponse, createID, contract.SummaryGrantRequestProcessed)
	require.Equal(t, contract.RequestCreated, created.Outcome)
	require.NotNil(t, created.Request)
	assert.Zero(t, catalog.CallCount())

	item := harness.GetGrantRequest(created.Request.ID)
	require.NotNil(t, item.Resource.SubmittedEvidence)
	approved := harness.ApproveGrantRequest(item, policy)
	assert.Equal(t, contract.RequestApproved, approved.Resource.State)
	assert.Zero(t, catalog.CallCount(), "approval must not replay the requested call")

	catalog.Fixture.SetCallOutcome(fixtureCallSuccess)
	retry := harness.ModernCall(issued.Bearer, json.RawMessage(`"smoke-retry"`), policy.Target, json.RawMessage(`{}`))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"smoke-retry","result":{"content":[{"type":"text","text":"fixture success"}]}}`, string(retry.Body))
	assert.Equal(t, 1, catalog.CallCount())

	result := harness.Stop(syscall.SIGTERM)
	assert.True(t, result.Cleanup.Reaped)
	assert.False(t, result.Cleanup.Survived)
}
