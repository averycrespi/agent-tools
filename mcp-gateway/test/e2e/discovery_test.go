//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayBinaryDeliversPrincipalSpecificDiscoveryAcrossBothEras(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer harness.Stop(syscall.SIGTERM)

	alphaNames := discoveryNames("alpha", 97)
	alphaTools := fixtureTools(alphaNames)
	alpha := harness.SetupCurrentCatalog("alpha", alphaTools)

	all := harness.CreatePrincipal("All tools", contract.VisibilityAll)
	allCredential := harness.IssueCredential(all)
	allDeny := harness.CreateGrant(grantSpec{
		PrincipalID: all.Resource.ID, Effect: contract.GrantDeny, ServerID: alpha.ServerID, UpstreamName: pointerTo("hidden"),
	})

	requestable := harness.CreatePrincipal("Requestable tools", contract.VisibilityRequestable)
	requestableCredential := harness.IssueCredential(requestable)
	requestableDeny := harness.CreateGrant(grantSpec{
		PrincipalID: requestable.Resource.ID, Effect: contract.GrantDeny, ServerID: alpha.ServerID, UpstreamName: pointerTo("hidden"),
	})

	allowedOnly := harness.CreatePrincipal("Allowed tools", contract.VisibilityAllowedOnly)
	allowedCredential := harness.IssueCredential(allowedOnly)
	events := harness.OpenEvents()
	require.Equal(t, http.StatusOK, events.StatusCode)
	reader := bufio.NewReader(events.Body)
	assert.Equal(t, ": keepalive\n\n", readSSEFrame(t, reader))
	constraint := json.RawMessage(`{"equals":{"/tenant":"blue"}}`)
	constrainedAllow := harness.CreateGrant(grantSpec{
		PrincipalID: allowedOnly.Resource.ID, Effect: contract.GrantAllow, ServerID: alpha.ServerID,
		UpstreamName: pointerTo("conditional"), Constraint: constraint,
	})
	assert.Equal(t, "event: invalidate\ndata: {\"kind\":\"authorization\",\"resource_id\":null}\n\n", readSSEFrame(t, reader))
	assert.Equal(t, "event: invalidate\ndata: {\"kind\":\"system_status\",\"resource_id\":null}\n\n", readSSEFrame(t, reader))
	require.NoError(t, events.Body.Close())

	modernDiscover := harness.ModernDiscover(allCredential.Bearer, json.RawMessage(`"modern-discover"`))
	require.Equal(t, http.StatusOK, modernDiscover.StatusCode, string(modernDiscover.Body))
	assert.Equal(t, `{"jsonrpc":"2.0","id":"modern-discover","result":{"resultType":"complete","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"mcp-gateway","version":"s1"}},"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28","2025-11-25","2025-06-18","2025-03-26","2024-11-05"],"capabilities":{"tools":{}}}}`, string(modernDiscover.Body))
	assert.NotContains(t, string(modernDiscover.Body), "listChanged")

	assertDiscoveryPage(t, harness.ModernList(allCredential.Bearer, json.RawMessage(`"modern-all"`), ""), json.RawMessage(`"modern-all"`), alphaNames, "")
	requestableNames := withoutName(alphaNames, "alpha.hidden")
	assertDiscoveryPage(t, harness.ModernList(requestableCredential.Bearer, json.RawMessage(`"modern-requestable"`), ""), json.RawMessage(`"modern-requestable"`), requestableNames, "")
	allowedResponse := harness.ModernList(allowedCredential.Bearer, json.RawMessage(`"modern-allowed"`), "")
	assertDiscoveryPage(t, allowedResponse, json.RawMessage(`"modern-allowed"`), []string{"alpha.conditional"}, "")
	for _, forbidden := range []string{allDeny.ID, requestableDeny.ID, constrainedAllow.ID, "constraint", "authorization_revision", "grant_id", "listChanged", `"_meta"`} {
		assert.NotContains(t, string(allowedResponse.Body), forbidden)
	}

	invalidModern := harness.ModernRequest(allowedCredential.Bearer, []byte(`{"jsonrpc":"2.0","id":"bad-modern","method":"tools/list","params":{"unknown":true,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"e2e-harness","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`))
	require.Equal(t, http.StatusOK, invalidModern.StatusCode, string(invalidModern.Body))
	assert.Equal(t, `{"jsonrpc":"2.0","id":"bad-modern","error":{"code":-32602,"message":"The tools/list parameters are invalid.","data":{"code":"invalid_params"}}}`, string(invalidModern.Body))

	beta := harness.SetupCurrentCatalog("beta", []fixtureTool{{Name: "shared", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	require.NotEqual(t, alpha.ServerID, beta.ServerID)
	legacySession, legacyInitialize := harness.LegacyInitialize(allCredential.Bearer, json.RawMessage(`1`))
	require.Equal(t, http.StatusOK, legacyInitialize.StatusCode, string(legacyInitialize.Body))
	assert.Equal(t, `{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"tools":{}},"protocolVersion":"2025-11-25","serverInfo":{"name":"mcp-gateway","version":"s1"}}}`, string(legacyInitialize.Body))
	assert.NotContains(t, string(legacyInitialize.Body), "listChanged")

	firstLegacy := harness.LegacyList(allCredential.Bearer, legacySession, json.RawMessage(`"legacy-first"`), "")
	firstCursor := discoveryCursor(t, firstLegacy)
	require.True(t, strings.HasPrefix(firstCursor, "mgw_dc1_"))
	assertDiscoveryPage(t, firstLegacy, json.RawMessage(`"legacy-first"`), alphaNames, firstCursor)
	secondLegacy := harness.LegacyList(allCredential.Bearer, legacySession, json.RawMessage(`"legacy-second"`), firstCursor)
	assertDiscoveryPage(t, secondLegacy, json.RawMessage(`"legacy-second"`), []string{"beta.shared"}, "")

	staleLegacy := harness.LegacyList(allCredential.Bearer, legacySession, json.RawMessage(`"bad-legacy"`), tamperDiscoveryCursor(t, firstCursor))
	require.Equal(t, http.StatusOK, staleLegacy.StatusCode, string(staleLegacy.Body))
	assert.Equal(t, `{"jsonrpc":"2.0","id":"bad-legacy","error":{"code":-32001,"message":"The tools/list cursor is stale.","data":{"code":"stale_cursor"}}}`, string(staleLegacy.Body))
	deleted := harness.LegacyDelete(allCredential.Bearer, legacySession)
	require.Equal(t, http.StatusNoContent, deleted.StatusCode, string(deleted.Body))

	statusResponse := harness.adminSnapshot(http.MethodGet, "/api/v1/system-status", nil)
	require.Equal(t, http.StatusOK, statusResponse.StatusCode, string(statusResponse.Body))
	assert.Equal(t, contract.MediaTypeJSON, statusResponse.Header.Get("Content-Type"))
	assert.Equal(t, "no-store", statusResponse.Header.Get("Cache-Control"))
	assert.Empty(t, statusResponse.Header.Get("Access-Control-Allow-Origin"))
	var status contract.SystemStatus
	decoder := json.NewDecoder(bytes.NewReader(statusResponse.Body))
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&status))
	var trailing any
	require.ErrorIs(t, decoder.Decode(&trailing), io.EOF)
	exactStatus, err := json.Marshal(status)
	require.NoError(t, err)
	assert.JSONEq(t, string(exactStatus), string(statusResponse.Body))
	assert.NotContains(t, string(statusResponse.Body), contract.AdminBearerPrefix)
	assert.NotContains(t, string(statusResponse.Body), contract.AgentBearerPrefix)
	assert.Equal(t, contract.ModernProtocolVersion, status.Protocols.Modern)
	assert.Equal(t, contract.LegacyProtocolVersion, status.Protocols.Legacy)
	assert.Equal(t, contract.AgentAuthPrincipalCredentials, status.Protocols.AgentAuth)
	assert.Equal(t, contract.LimitStatus{InUse: 3, Limit: 128}, status.Limits.Principals)
	assert.Equal(t, contract.LimitStatus{InUse: 6, Limit: 4096}, status.Limits.Grants)
	assert.Equal(t, contract.LimitStatus{InUse: 101, Limit: 2048}, status.Limits.ActiveTools)
	assert.Equal(t, contract.LimitStatus{InUse: 2, Limit: 32}, status.Limits.DownstreamRuntimes)
	assert.Equal(t, contract.LimitStatus{InUse: 0, Limit: 128}, status.Limits.LegacySessions)
}

func discoveryNames(namespace string, numbered int) []string {
	names := []string{namespace + ".conditional", namespace + ".hidden", namespace + ".shared"}
	for index := range numbered {
		names = append(names, fmt.Sprintf("%s.tool-%03d", namespace, index))
	}
	return names
}

func fixtureTools(externalNames []string) []fixtureTool {
	tools := make([]fixtureTool, 0, len(externalNames))
	for _, externalName := range externalNames {
		_, upstream, found := strings.Cut(externalName, ".")
		if !found {
			upstream = externalName
		}
		tools = append(tools, fixtureTool{Name: upstream, InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
	return tools
}

func withoutName(names []string, excluded string) []string {
	result := make([]string, 0, len(names)-1)
	for _, name := range names {
		if name != excluded {
			result = append(result, name)
		}
	}
	return result
}

func readSSEFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	const maximumFrameBytes = 4096
	var frame strings.Builder
	for {
		line, err := reader.ReadSlice('\n')
		require.NoError(t, err)
		require.LessOrEqual(t, frame.Len()+len(line), maximumFrameBytes, "SSE frame exceeded harness bound")
		_, _ = frame.Write(line)
		if bytes.Equal(line, []byte("\n")) {
			return frame.String()
		}
	}
}

func assertDiscoveryPage(t *testing.T, response responseSnapshot, id json.RawMessage, names []string, cursor string) {
	t.Helper()
	require.Equal(t, http.StatusOK, response.StatusCode, string(response.Body))
	assert.Equal(t, expectedDiscoveryPage(id, names, cursor), string(response.Body))
	assert.NotContains(t, string(response.Body), "listChanged")
}

func expectedDiscoveryPage(id json.RawMessage, names []string, cursor string) string {
	var result strings.Builder
	result.WriteString(`{"jsonrpc":"2.0","id":`)
	result.Write(id)
	result.WriteString(`,"result":{"tools":[`)
	for index, name := range names {
		if index > 0 {
			result.WriteByte(',')
		}
		encodedName, _ := json.Marshal(name)
		result.WriteString(`{"annotations":{"destructiveHint":true,"idempotentHint":false,"openWorldHint":true,"readOnlyHint":false},"inputSchema":{"type":"object"},"name":`)
		result.Write(encodedName)
		result.WriteByte('}')
	}
	result.WriteByte(']')
	if cursor != "" {
		encodedCursor, _ := json.Marshal(cursor)
		result.WriteString(`,"nextCursor":`)
		result.Write(encodedCursor)
	}
	result.WriteString(`}}`)
	return result.String()
}

func discoveryCursor(t *testing.T, response responseSnapshot) string {
	t.Helper()
	var envelope struct {
		Result struct {
			NextCursor string `json:"nextCursor"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.Body, &envelope))
	require.NotEmpty(t, envelope.Result.NextCursor)
	return envelope.Result.NextCursor
}

func tamperDiscoveryCursor(t *testing.T, cursor string) string {
	t.Helper()
	const prefix = "mgw_dc1_"
	require.True(t, strings.HasPrefix(cursor, prefix))
	frame, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, prefix))
	require.NoError(t, err)
	require.NotEmpty(t, frame)
	frame[len(frame)/2] ^= 0x01
	return prefix + base64.RawURLEncoding.EncodeToString(frame)
}
