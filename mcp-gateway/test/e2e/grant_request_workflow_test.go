//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2EGrantRequestWorkflow(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer func() {
		if harness.process != nil {
			harness.Stop(syscall.SIGTERM)
		}
	}()
	catalog := harness.SetupCurrentCatalog("s5-workflow", []fixtureTool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	principal := harness.CreatePrincipal("S5 workflow agent", contract.VisibilityRequestable)
	issued := harness.IssueCredential(principal)
	foreignPrincipal := harness.CreatePrincipal("S5 foreign agent", contract.VisibilityRequestable)
	foreign := harness.IssueCredential(foreignPrincipal)

	identityID := json.RawMessage(`"modern-identity"`)
	identityResponse := harness.ModernSelfServiceCall(issued.Bearer, identityID, "get_identity", struct{}{})
	identity := decodeSelfServiceResult[contract.GetIdentityResult](t, identityResponse, identityID, contract.SummaryIdentityReturned)
	assert.Equal(t, principal.Resource.ID, identity.Identity.ID)

	grantsID := json.RawMessage(`"modern-grants"`)
	grantsResponse := harness.ModernSelfServiceCall(issued.Bearer, grantsID, "list_grants", contract.ListGrantsInput{})
	selfGrants := decodeSelfServiceResult[contract.ListGrantsResult](t, grantsResponse, grantsID, contract.SummaryGrantsReturned)
	require.Equal(t, contract.CursorOK, selfGrants.Outcome)
	require.Len(t, selfGrants.Items, 1)
	assert.Equal(t, contract.SyntheticServerNamespace, selfGrants.Items[0].Policy.Target)

	exactPolicy := contract.Policy{Scope: contract.PolicyTool, Target: "s5-workflow.echo"}
	createID := json.RawMessage(`"modern-create-exact"`)
	createResponse := harness.ModernSelfServiceCall(issued.Bearer, createID, "create_grant_request", contract.CreateGrantRequestInput{Policy: exactPolicy})
	created := decodeSelfServiceResult[contract.CreateGrantRequestResult](t, createResponse, createID, contract.SummaryGrantRequestProcessed)
	require.Equal(t, contract.RequestCreated, created.Outcome)
	require.NotNil(t, created.Request)
	assert.Zero(t, catalog.CallCount(), "request creation must not call downstream")

	legacySession, initialized := harness.LegacyInitialize(issued.Bearer, json.RawMessage(`1`))
	require.Equal(t, http.StatusOK, initialized.StatusCode)
	duplicateID := json.RawMessage(`"legacy-existing"`)
	duplicateResponse := harness.LegacySelfServiceCall(issued.Bearer, legacySession, duplicateID, "create_grant_request", contract.CreateGrantRequestInput{Policy: exactPolicy})
	duplicate := decodeSelfServiceResult[contract.CreateGrantRequestResult](t, duplicateResponse, duplicateID, contract.SummaryGrantRequestProcessed)
	assert.Equal(t, contract.RequestExisting, duplicate.Outcome)
	require.NotNil(t, duplicate.Request)
	assert.Equal(t, created.Request.ID, duplicate.Request.ID)

	getID := json.RawMessage(`"modern-get"`)
	getResponse := harness.ModernSelfServiceCall(issued.Bearer, getID, "get_grant_request", contract.GrantRequestIDInput{ID: created.Request.ID})
	got := decodeSelfServiceResult[contract.GetGrantRequestResult](t, getResponse, getID, contract.SummaryGrantRequestReturned)
	assert.Equal(t, contract.RequestFound, got.Outcome)
	require.NotNil(t, got.Request)
	assert.Equal(t, created.Request.ID, got.Request.ID)

	foreignID := json.RawMessage(`"foreign-get"`)
	foreignResponse := harness.ModernSelfServiceCall(foreign.Bearer, foreignID, "get_grant_request", contract.GrantRequestIDInput{ID: created.Request.ID})
	foreignGet := decodeSelfServiceResult[contract.GetGrantRequestResult](t, foreignResponse, foreignID, contract.SummaryGrantRequestReturned)
	assert.Equal(t, contract.RequestNotFound, foreignGet.Outcome)
	assert.Nil(t, foreignGet.Request)

	listID := json.RawMessage(`"legacy-list"`)
	listResponse := harness.LegacySelfServiceCall(issued.Bearer, legacySession, listID, "list_grant_requests", contract.ListGrantRequestsInput{})
	listed := decodeSelfServiceResult[contract.ListGrantRequestsResult](t, listResponse, listID, contract.SummaryGrantRequestsReturned)
	require.Equal(t, contract.CursorOK, listed.Outcome)
	require.Len(t, listed.Items, 1)
	assert.Equal(t, created.Request.ID, listed.Items[0].ID)

	duration60 := "60"
	serverPolicy := contract.Policy{Scope: contract.PolicyServer, Target: catalog.Namespace, DurationSeconds: &duration60, FutureToolsAcknowledged: true}
	cancelCreateID := json.RawMessage(`"legacy-create-cancel"`)
	cancelCreateResponse := harness.LegacySelfServiceCall(issued.Bearer, legacySession, cancelCreateID, "create_grant_request", contract.CreateGrantRequestInput{Policy: serverPolicy})
	cancelCreated := decodeSelfServiceResult[contract.CreateGrantRequestResult](t, cancelCreateResponse, cancelCreateID, contract.SummaryGrantRequestProcessed)
	require.Equal(t, contract.RequestCreated, cancelCreated.Outcome)
	require.NotNil(t, cancelCreated.Request)
	cancelID := json.RawMessage(`"legacy-cancel"`)
	cancelResponse := harness.LegacySelfServiceCall(issued.Bearer, legacySession, cancelID, "cancel_grant_request", contract.GrantRequestIDInput{ID: cancelCreated.Request.ID})
	cancelled := decodeSelfServiceResult[contract.CancelGrantRequestResult](t, cancelResponse, cancelID, contract.SummaryGrantRequestCancellationProcessed)
	assert.Equal(t, contract.RequestCancellationCancelled, cancelled.Outcome)
	repeatCancelID := json.RawMessage(`"modern-cancel-repeat"`)
	repeatCancelResponse := harness.ModernSelfServiceCall(issued.Bearer, repeatCancelID, "cancel_grant_request", contract.GrantRequestIDInput{ID: cancelCreated.Request.ID})
	repeated := decodeSelfServiceResult[contract.CancelGrantRequestResult](t, repeatCancelResponse, repeatCancelID, contract.SummaryGrantRequestCancellationProcessed)
	assert.Equal(t, contract.RequestCancellationAlreadyCancelled, repeated.Outcome)

	summaries := harness.ListGrantRequests(principal.Resource.ID)
	require.Len(t, summaries, 2)
	adminItem := harness.GetGrantRequest(created.Request.ID)
	require.NotNil(t, adminItem.Resource.SubmittedEvidence)
	assertDescriptorEvidenceMatches(t, adminItem.Resource.SubmittedEvidence, harness.GetOnlyDescriptor(catalog.ServerID), catalog.Namespace)

	narrowedPolicy := exactPolicy
	narrowedPolicy.DurationSeconds = &duration60
	approved := harness.ApproveGrantRequest(adminItem, narrowedPolicy)
	assert.Equal(t, contract.RequestApproved, approved.Resource.State)
	require.NotNil(t, approved.Resource.ApprovedGrantID)
	assert.Zero(t, catalog.CallCount(), "approval must not submit or replay the original call")

	catalog.Fixture.SetCallOutcome(fixtureCallSuccess)
	retry := harness.ModernCall(issued.Bearer, json.RawMessage(`"explicit-retry"`), "s5-workflow.echo", json.RawMessage(`{}`))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"explicit-retry","result":{"content":[{"type":"text","text":"fixture success"}]}}`, string(retry.Body))
	assert.Equal(t, 1, catalog.CallCount(), "only the explicit fresh call may reach downstream")

	duration120 := "120"
	rejectPolicy := contract.Policy{Scope: contract.PolicyServer, Target: catalog.Namespace, DurationSeconds: &duration120, FutureToolsAcknowledged: true}
	rejectCreateID := json.RawMessage(`"modern-create-reject"`)
	rejectCreateResponse := harness.ModernSelfServiceCall(issued.Bearer, rejectCreateID, "create_grant_request", contract.CreateGrantRequestInput{Policy: rejectPolicy})
	rejectCreated := decodeSelfServiceResult[contract.CreateGrantRequestResult](t, rejectCreateResponse, rejectCreateID, contract.SummaryGrantRequestProcessed)
	require.NotNil(t, rejectCreated.Request)
	rejected := harness.RejectGrantRequest(harness.GetGrantRequest(rejectCreated.Request.ID), contract.RejectionNotApproved)
	assert.Equal(t, contract.RequestRejected, rejected.Resource.State)

	deny := harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantDeny, ServerID: catalog.ServerID, UpstreamName: pointerTo("echo")})
	conflictID := json.RawMessage(`"modern-deny-conflict"`)
	conflictResponse := harness.ModernSelfServiceCall(issued.Bearer, conflictID, "create_grant_request", contract.CreateGrantRequestInput{Policy: contract.Policy{Scope: contract.PolicyTool, Target: "s5-workflow.echo", DurationSeconds: &duration60}})
	conflict := decodeSelfServiceResult[contract.CreateGrantRequestResult](t, conflictResponse, conflictID, contract.SummaryGrantRequestProcessed)
	assert.Equal(t, contract.RequestDenyConflict, conflict.Outcome)
	harness.DeleteGrant(deny.ID)

	defaultGrant := harness.ListGrants(principal.Resource.ID, contract.SyntheticServerID)
	require.Len(t, defaultGrant, 1)
	harness.DeleteGrant(defaultGrant[0].ID)
	assertCallRejected(t, harness.ModernSelfServiceCall(issued.Bearer, json.RawMessage(`"default-removed"`), "get_identity", struct{}{}), json.RawMessage(`"default-removed"`))
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: contract.SyntheticServerID})
	restoredID := json.RawMessage(`"default-restored"`)
	restoredResponse := harness.ModernSelfServiceCall(issued.Bearer, restoredID, "get_identity", struct{}{})
	restored := decodeSelfServiceResult[contract.GetIdentityResult](t, restoredResponse, restoredID, contract.SummaryIdentityReturned)
	assert.Equal(t, principal.Resource.ID, restored.Identity.ID)

	finalListID := json.RawMessage(`"modern-final-list"`)
	finalListResponse := harness.ModernSelfServiceCall(issued.Bearer, finalListID, "list_grant_requests", contract.ListGrantRequestsInput{})
	finalList := decodeSelfServiceResult[contract.ListGrantRequestsResult](t, finalListResponse, finalListID, contract.SummaryGrantRequestsReturned)
	require.Len(t, finalList.Items, 3)
	assert.Equal(t, http.StatusNoContent, harness.LegacyDelete(issued.Bearer, legacySession).StatusCode)
}
