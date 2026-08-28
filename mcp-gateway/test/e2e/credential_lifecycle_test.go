//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"sort"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayBinaryIsolatesCredentialLifecycleAndRestartAuthority(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer harness.Stop(syscall.SIGTERM)

	names := discoveryNames("lifecycle", 98)
	catalog := harness.SetupCurrentCatalog("lifecycle", fixtureTools(names))

	principalA := harness.CreatePrincipal("Principal A", contract.VisibilityRequestable)
	assertPrincipalVersion(t, principalA, contract.PrincipalActive, "1", "0", false)
	credentialA1 := harness.IssueCredential(principalA)
	assertPrincipalVersion(t, credentialA1.Principal, contract.PrincipalActive, "2", "1", true)
	credentialA1ID := credentialA1.Principal.Resource.Credential.ID

	principalB := harness.CreatePrincipal("Principal B", contract.VisibilityRequestable)
	assertPrincipalVersion(t, principalB, contract.PrincipalActive, "1", "0", false)
	credentialB1 := harness.IssueCredential(principalB)
	assertPrincipalVersion(t, credentialB1.Principal, contract.PrincipalActive, "2", "1", true)
	credentialB1Resource := *credentialB1.Principal.Resource.Credential

	sessionA1, _ := harness.LegacyInitialize(credentialA1.Bearer, json.RawMessage(`"a1"`))
	firstA1 := harness.LegacyList(credentialA1.Bearer, sessionA1, json.RawMessage(`"a1-first"`), "")
	cursorA1 := discoveryCursor(t, firstA1)
	assertDiscoveryPage(t, firstA1, json.RawMessage(`"a1-first"`), names[:100], cursorA1)
	sessionB1, _ := harness.LegacyInitialize(credentialB1.Bearer, json.RawMessage(`"b1"`))
	assertLegacyNames(t, harness, credentialB1.Bearer, sessionB1, names, "b-before-policy")

	deny := harness.CreateGrant(grantSpec{
		PrincipalID: principalA.Resource.ID, Effect: contract.GrantDeny, ServerID: catalog.ServerID, UpstreamName: pointerTo("hidden"),
	})
	stalePolicy := harness.LegacyList(credentialA1.Bearer, sessionA1, json.RawMessage(`"policy-stale"`), cursorA1)
	assertRPCError(t, stalePolicy, `{"jsonrpc":"2.0","id":"policy-stale","error":{"code":-32001,"message":"The tools/list cursor is stale.","data":{"code":"stale_cursor"}}}`)
	withoutHidden := withoutName(names, "lifecycle.hidden")
	assertLegacyNames(t, harness, credentialA1.Bearer, sessionA1, withoutHidden, "a-denied")
	assertLegacyNames(t, harness, credentialB1.Bearer, sessionB1, names, "b-policy-continuity")
	assertPrincipalVersion(t, harness.GetPrincipal(principalA.Resource.ID), contract.PrincipalActive, "2", "1", true)
	assertPrincipalBUnchanged(t, harness, credentialB1.Principal, credentialB1Resource)
	harness.DeleteGrant(deny.ID)
	assertModernNames(t, harness, credentialA1.Bearer, names, "a-policy-restored")

	credentialA2 := harness.IssueCredential(credentialA1.Principal)
	assertPrincipalVersion(t, credentialA2.Principal, contract.PrincipalActive, "3", "2", true)
	require.NotEqual(t, credentialA1ID, credentialA2.Principal.Resource.Credential.ID)
	assertAuthenticationProblem(t, harness.ModernList(credentialA1.Bearer, json.RawMessage(`"old-a1"`), ""))
	assertNotFoundProblem(t, harness.LegacyList(credentialA2.Bearer, sessionA1, json.RawMessage(`"old-session-a1"`), ""))
	assertPrincipalBUnchanged(t, harness, credentialB1.Principal, credentialB1Resource)
	assertLegacyNames(t, harness, credentialB1.Bearer, sessionB1, names, "b-after-replace")

	sessionA2, _ := harness.LegacyInitialize(credentialA2.Bearer, json.RawMessage(`"a2"`))
	revokedA2 := harness.RevokeCredential(credentialA2.Principal)
	assertPrincipalVersion(t, revokedA2, contract.PrincipalActive, "4", "3", false)
	assertAuthenticationProblem(t, harness.ModernList(credentialA2.Bearer, json.RawMessage(`"revoked-a2"`), ""))
	credentialA3 := harness.IssueCredential(revokedA2)
	assertPrincipalVersion(t, credentialA3.Principal, contract.PrincipalActive, "5", "4", true)
	assertNotFoundProblem(t, harness.LegacyList(credentialA3.Bearer, sessionA2, json.RawMessage(`"old-session-a2"`), ""))
	assertPrincipalBUnchanged(t, harness, credentialB1.Principal, credentialB1Resource)

	sessionA3, _ := harness.LegacyInitialize(credentialA3.Bearer, json.RawMessage(`"a3"`))
	disabledState := contract.PrincipalDisabled
	disabledA := harness.PatchPrincipal(credentialA3.Principal, principalPatch{State: &disabledState})
	assertPrincipalVersion(t, disabledA, contract.PrincipalDisabled, "6", "5", false)
	assertAuthenticationProblem(t, harness.ModernList(credentialA3.Bearer, json.RawMessage(`"disabled-a3"`), ""))
	disabledIssue := harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v1/principals/"+disabledA.Resource.ID+"/credential", []byte(`{}`), map[string]string{"If-Match": disabledA.ETag})
	assertProblem(t, disabledIssue, http.StatusConflict, "conflict", "The request conflicts with current state.", false)
	assertPrincipalVersion(t, harness.GetPrincipal(principalA.Resource.ID), contract.PrincipalDisabled, "6", "5", false)

	activeState := contract.PrincipalActive
	reenabledA := harness.PatchPrincipal(disabledA, principalPatch{State: &activeState})
	assertPrincipalVersion(t, reenabledA, contract.PrincipalActive, "7", "5", false)
	assertAuthenticationProblem(t, harness.ModernList(credentialA3.Bearer, json.RawMessage(`"reenabled-old-a3"`), ""))
	credentialA4 := harness.IssueCredential(reenabledA)
	assertPrincipalVersion(t, credentialA4.Principal, contract.PrincipalActive, "8", "6", true)
	assertNotFoundProblem(t, harness.LegacyList(credentialA4.Bearer, sessionA3, json.RawMessage(`"old-session-a3"`), ""))
	assertPrincipalBUnchanged(t, harness, credentialB1.Principal, credentialB1Resource)
	assertLegacyNames(t, harness, credentialB1.Bearer, sessionB1, names, "b-after-a-lifecycle")

	restartSessionA, _ := harness.LegacyInitialize(credentialA4.Bearer, json.RawMessage(`"restart-a"`))
	restartSessionB, _ := harness.LegacyInitialize(credentialB1.Bearer, json.RawMessage(`"restart-b"`))
	beforeRestart := harness.ModernList(credentialA4.Bearer, json.RawMessage(`"before-restart"`), "")
	restartCursor := discoveryCursor(t, beforeRestart)
	assertDiscoveryPage(t, beforeRestart, json.RawMessage(`"before-restart"`), names[:100], restartCursor)
	principalABeforeRestart := credentialA4.Principal.Resource

	harness.Stop(syscall.SIGTERM)
	harness.Start()
	waitForStdioServer(t, harness, catalog.ServerID, activeCatalog)

	principalAAfterRestart := harness.GetPrincipal(principalA.Resource.ID)
	assertPrincipalVersion(t, principalAAfterRestart, contract.PrincipalActive, "8", "6", true)
	assert.Equal(t, principalABeforeRestart.Credential, principalAAfterRestart.Resource.Credential)
	assertPrincipalBUnchanged(t, harness, credentialB1.Principal, credentialB1Resource)
	assertModernNames(t, harness, credentialA4.Bearer, names, "a-after-restart")
	assertModernNames(t, harness, credentialB1.Bearer, names, "b-after-restart")
	assertNotFoundProblem(t, harness.LegacyList(credentialA4.Bearer, restartSessionA, json.RawMessage(`"old-restart-a"`), ""))
	assertNotFoundProblem(t, harness.LegacyList(credentialB1.Bearer, restartSessionB, json.RawMessage(`"old-restart-b"`), ""))
	staleRestart := harness.ModernList(credentialA4.Bearer, json.RawMessage(`"stale-restart"`), restartCursor)
	assertRPCError(t, staleRestart, `{"jsonrpc":"2.0","id":"stale-restart","error":{"code":-32001,"message":"The tools/list cursor is stale.","data":{"code":"stale_cursor"}}}`)
	assertModernNames(t, harness, credentialA4.Bearer, names, "a-fresh-relist")
}

func assertPrincipalVersion(t *testing.T, principal principalHandle, state contract.PrincipalState, principalRevision, credentialRevision string, credentialPresent bool) {
	t.Helper()
	assert.Equal(t, state, principal.Resource.State)
	assert.Equal(t, principalRevision, principal.Resource.Revision)
	assert.Equal(t, credentialRevision, principal.Resource.CredentialRevision)
	assert.Equal(t, contract.PrincipalETag(principal.Resource.ID, principalRevision), principal.ETag)
	if credentialPresent {
		require.NotNil(t, principal.Resource.Credential)
		assert.Equal(t, credentialRevision, principal.Resource.Credential.Revision)
	} else {
		assert.Nil(t, principal.Resource.Credential)
	}
}

func assertPrincipalBUnchanged(t *testing.T, harness *gatewayHarness, original principalHandle, credential contract.AgentCredential) {
	t.Helper()
	current := harness.GetPrincipal(original.Resource.ID)
	assertPrincipalVersion(t, current, contract.PrincipalActive, "2", "1", true)
	assert.Equal(t, credential, *current.Resource.Credential)
	assert.Equal(t, original.ETag, current.ETag)
}

func withSyntheticNames(names []string) []string {
	visible := append([]string(nil), names...)
	for _, tool := range contract.SyntheticSelfServiceTools() {
		visible = append(visible, tool.ExternalName)
	}
	sort.Strings(visible)
	return visible
}

func assertModernNames(t *testing.T, harness *gatewayHarness, bearer *agentBearer, names []string, idPrefix string) {
	t.Helper()
	names = withSyntheticNames(names)
	firstID := json.RawMessage(`"` + idPrefix + `-first"`)
	first := harness.ModernList(bearer, firstID, "")
	if len(names) <= 100 {
		assertDiscoveryNamePage(t, first, names, "")
		return
	}
	cursor := discoveryCursor(t, first)
	assertDiscoveryNamePage(t, first, names[:100], cursor)
	secondID := json.RawMessage(`"` + idPrefix + `-second"`)
	second := harness.ModernList(bearer, secondID, cursor)
	assertDiscoveryNamePage(t, second, names[100:], "")
}

func assertLegacyNames(t *testing.T, harness *gatewayHarness, bearer *agentBearer, session legacySessionHandle, names []string, idPrefix string) {
	t.Helper()
	names = withSyntheticNames(names)
	firstID := json.RawMessage(`"` + idPrefix + `-first"`)
	first := harness.LegacyList(bearer, session, firstID, "")
	if len(names) <= 100 {
		assertDiscoveryNamePage(t, first, names, "")
		return
	}
	cursor := discoveryCursor(t, first)
	assertDiscoveryNamePage(t, first, names[:100], cursor)
	secondID := json.RawMessage(`"` + idPrefix + `-second"`)
	second := harness.LegacyList(bearer, session, secondID, cursor)
	assertDiscoveryNamePage(t, second, names[100:], "")
}

func assertDiscoveryNamePage(t *testing.T, response responseSnapshot, expectedNames []string, expectedCursor string) {
	t.Helper()
	require.Equal(t, http.StatusOK, response.StatusCode, string(response.Body))
	var envelope struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.Body, &envelope))
	names := make([]string, len(envelope.Result.Tools))
	for index, tool := range envelope.Result.Tools {
		names[index] = tool.Name
	}
	assert.Equal(t, expectedNames, names)
	assert.Equal(t, expectedCursor, envelope.Result.NextCursor)
	assert.NotContains(t, string(response.Body), "listChanged")
}

func assertAuthenticationProblem(t *testing.T, response responseSnapshot) {
	t.Helper()
	assertProblem(t, response, http.StatusUnauthorized, "authentication_required", "Authentication is required.", true)
}

func assertNotFoundProblem(t *testing.T, response responseSnapshot) {
	t.Helper()
	assertProblem(t, response, http.StatusNotFound, "not_found", "The resource was not found.", false)
}

func assertProblem(t *testing.T, response responseSnapshot, status int, code, title string, authenticate bool) {
	t.Helper()
	assert.Equal(t, status, response.StatusCode)
	assert.Equal(t, contract.MediaTypeProblemJSON, response.Header.Get("Content-Type"))
	assert.Equal(t, `{"status":`+jsonNumber(status)+`,"code":"`+code+`","title":"`+title+`"}`+"\n", string(response.Body))
	if authenticate {
		assert.Equal(t, "Bearer", response.Header.Get("WWW-Authenticate"))
	} else {
		assert.Empty(t, response.Header.Get("WWW-Authenticate"))
	}
}

func assertRPCError(t *testing.T, response responseSnapshot, expected string) {
	t.Helper()
	require.Equal(t, http.StatusOK, response.StatusCode, string(response.Body))
	assert.Equal(t, expected, string(response.Body))
}

func jsonNumber(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
