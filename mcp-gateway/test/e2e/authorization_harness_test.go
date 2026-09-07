//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

const harnessResponseLimit = 4 * 1024 * 1024

type responseSnapshot struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

type principalHandle struct {
	Resource contract.Principal
	ETag     string
}

type issuedAgentCredential struct {
	Principal principalHandle
	Bearer    *agentBearer
}

type agentBearer struct {
	authorization func() string
	scan          func(string, io.Reader) error
	destroy       func()
}

func newAgentBearer(t *testing.T, value string) *agentBearer {
	t.Helper()
	require.True(t, strings.HasPrefix(value, contract.AgentBearerPrefix))
	secret := []byte(value)
	scanner, err := testutil.NewCanaryScanner(secret)
	require.NoError(t, err)
	bearer := &agentBearer{
		authorization: func() string { return "Bearer " + string(secret) },
		scan:          scanner.Scan,
		destroy: func() {
			for index := range secret {
				secret[index] = 0
			}
			secret = nil
		},
	}
	t.Cleanup(bearer.clear)
	return bearer
}

func (bearer *agentBearer) authorizationHeader() string {
	return bearer.authorization()
}

func (bearer *agentBearer) clear() {
	if bearer.destroy != nil {
		bearer.destroy()
		bearer.authorization = nil
		bearer.scan = nil
		bearer.destroy = nil
	}
}

func (*agentBearer) String() string { return "[redacted agent bearer]" }

func (*agentBearer) GoString() string { return "[redacted agent bearer]" }

func (*agentBearer) MarshalJSON() ([]byte, error) {
	return []byte(`"[redacted agent bearer]"`), nil
}

func (bearer *agentBearer) assertAbsent(t *testing.T, sink string, reader io.Reader) {
	t.Helper()
	require.NoError(t, bearer.scan(sink, reader))
}

type principalPatch struct {
	DisplayName *string
	State       *contract.PrincipalState
	Visibility  *contract.PrincipalVisibility
}

type grantSpec struct {
	Description  string
	PrincipalID  string
	Effect       contract.GrantEffect
	ServerID     string
	UpstreamName *string
	Constraint   json.RawMessage
	ExpiresAt    *string
}

type currentCatalogHandle struct {
	ServerID  string
	ETag      string
	Namespace string
	Fixture   *rawHTTPFixture
}

type legacySessionHandle struct {
	ID string
}

type auditObservation struct {
	Sequence       int64
	InvocationID   string
	AdmissionClass contract.InvocationAdmissionClass
	Decision       contract.AuthorizationDecision
	TerminalClass  contract.InvocationTerminalClass
}

func (harness *gatewayHarness) requestSnapshot(method, path string, body []byte, headers map[string]string) responseSnapshot {
	harness.t.Helper()
	request, err := http.NewRequestWithContext(harness.ctx, method, "http://"+harness.authority+path, bytes.NewReader(body))
	require.NoError(harness.t, err)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := harness.client.Do(request)
	require.NoError(harness.t, err)
	contents, err := io.ReadAll(io.LimitReader(response.Body, harnessResponseLimit+1))
	require.NoError(harness.t, err)
	require.NoError(harness.t, response.Body.Close())
	require.LessOrEqual(harness.t, len(contents), harnessResponseLimit, "response exceeded harness bound")
	return responseSnapshot{StatusCode: response.StatusCode, Header: response.Header.Clone(), Body: contents}
}

func (harness *gatewayHarness) adminSnapshot(method, path string, body []byte) responseSnapshot {
	harness.t.Helper()
	headers := map[string]string{"Authorization": "Bearer " + harness.bearer}
	if len(body) > 0 {
		headers["Content-Type"] = contract.MediaTypeJSON
	}
	return harness.requestSnapshot(method, path, body, headers)
}

func decodeSnapshot(t *testing.T, response responseSnapshot, status int, target any) {
	t.Helper()
	require.Equal(t, status, response.StatusCode, string(response.Body))
	if target != nil {
		require.NoError(t, json.Unmarshal(response.Body, target))
	}
}

func (harness *gatewayHarness) CreatePrincipal(displayName string, visibility contract.PrincipalVisibility) principalHandle {
	harness.t.Helper()
	body, err := json.Marshal(struct {
		DisplayName string                       `json:"display_name"`
		Visibility  contract.PrincipalVisibility `json:"visibility"`
	}{DisplayName: displayName, Visibility: visibility})
	require.NoError(harness.t, err)
	response := harness.adminSnapshot(http.MethodPost, "/api/v1/principals", body)
	var creation contract.PrincipalCreation
	decodeSnapshot(harness.t, response, http.StatusCreated, &creation)
	return checkedPrincipal(harness.t, creation.Principal, response.Header.Get("ETag"))
}

func (harness *gatewayHarness) GetPrincipal(principalID string) principalHandle {
	harness.t.Helper()
	response := harness.adminSnapshot(http.MethodGet, "/api/v1/principals/"+url.PathEscape(principalID), nil)
	var principal contract.Principal
	decodeSnapshot(harness.t, response, http.StatusOK, &principal)
	return checkedPrincipal(harness.t, principal, response.Header.Get("ETag"))
}

func (harness *gatewayHarness) PatchPrincipal(current principalHandle, patch principalPatch) principalHandle {
	harness.t.Helper()
	body := make(map[string]any)
	if patch.DisplayName != nil {
		body["display_name"] = *patch.DisplayName
	}
	if patch.State != nil {
		body["state"] = *patch.State
	}
	if patch.Visibility != nil {
		body["visibility"] = *patch.Visibility
	}
	contents, err := json.Marshal(body)
	require.NoError(harness.t, err)
	response := harness.adminSnapshotWithHeaders(http.MethodPatch, "/api/v1/principals/"+url.PathEscape(current.Resource.ID), contents, map[string]string{"If-Match": current.ETag})
	var principal contract.Principal
	decodeSnapshot(harness.t, response, http.StatusOK, &principal)
	return checkedPrincipal(harness.t, principal, response.Header.Get("ETag"))
}

func (harness *gatewayHarness) IssueCredential(current principalHandle) issuedAgentCredential {
	harness.t.Helper()
	response := harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v1/principals/"+url.PathEscape(current.Resource.ID)+"/credential", []byte(`{}`), map[string]string{"If-Match": current.ETag})
	defer clear(response.Body)
	require.Equal(harness.t, http.StatusCreated, response.StatusCode)
	var creation contract.AgentCredentialCreation
	require.NoError(harness.t, json.Unmarshal(response.Body, &creation))
	require.Equal(harness.t, 1, bytes.Count(response.Body, []byte(creation.Bearer)))
	bearer := newAgentBearer(harness.t, creation.Bearer)
	creation.Bearer = ""
	return issuedAgentCredential{Principal: checkedPrincipal(harness.t, creation.Principal, response.Header.Get("ETag")), Bearer: bearer}
}

func (harness *gatewayHarness) RevokeCredential(current principalHandle) principalHandle {
	harness.t.Helper()
	response := harness.adminSnapshotWithHeaders(http.MethodDelete, "/api/v1/principals/"+url.PathEscape(current.Resource.ID)+"/credential", []byte(`{}`), map[string]string{"If-Match": current.ETag})
	var principal contract.Principal
	decodeSnapshot(harness.t, response, http.StatusOK, &principal)
	return checkedPrincipal(harness.t, principal, response.Header.Get("ETag"))
}

func (harness *gatewayHarness) adminSnapshotWithHeaders(method, path string, body []byte, extra map[string]string) responseSnapshot {
	harness.t.Helper()
	headers := map[string]string{"Authorization": "Bearer " + harness.bearer}
	if len(body) > 0 {
		headers["Content-Type"] = contract.MediaTypeJSON
	}
	for name, value := range extra {
		headers[name] = value
	}
	return harness.requestSnapshot(method, path, body, headers)
}

func checkedPrincipal(t *testing.T, principal contract.Principal, etag string) principalHandle {
	t.Helper()
	require.Equal(t, contract.PrincipalETag(principal.ID, principal.Revision), etag)
	return principalHandle{Resource: principal, ETag: etag}
}

func (harness *gatewayHarness) CreateGrant(spec grantSpec) contract.Grant {
	harness.t.Helper()
	if spec.Description == "" {
		spec.Description = "Test grant"
	}
	body, err := json.Marshal(struct {
		Description  string          `json:"description"`
		PrincipalID  string          `json:"principal_id"`
		Effect       string          `json:"effect"`
		ServerID     string          `json:"server_id"`
		UpstreamName *string         `json:"upstream_name"`
		Constraint   json.RawMessage `json:"constraint"`
		ExpiresAt    *string         `json:"expires_at"`
	}{Description: spec.Description, PrincipalID: spec.PrincipalID, Effect: string(spec.Effect), ServerID: spec.ServerID, UpstreamName: spec.UpstreamName, Constraint: spec.Constraint, ExpiresAt: spec.ExpiresAt})
	require.NoError(harness.t, err)
	response := harness.adminSnapshot(http.MethodPost, "/api/v1/grants", body)
	var grant contract.Grant
	decodeSnapshot(harness.t, response, http.StatusCreated, &grant)
	return grant
}

func (harness *gatewayHarness) GetGrant(grantID string) contract.Grant {
	harness.t.Helper()
	response := harness.adminSnapshot(http.MethodGet, "/api/v1/grants/"+url.PathEscape(grantID), nil)
	var grant contract.Grant
	decodeSnapshot(harness.t, response, http.StatusOK, &grant)
	return grant
}

func (harness *gatewayHarness) ListGrants(principalID, serverID string) []contract.Grant {
	harness.t.Helper()
	query := url.Values{"limit": {"100"}}
	if principalID != "" {
		query.Set("principal_id", principalID)
	}
	if serverID != "" {
		query.Set("server_id", serverID)
	}
	response := harness.adminSnapshot(http.MethodGet, "/api/v1/grants?"+query.Encode(), nil)
	var page contract.Collection[contract.Grant]
	decodeSnapshot(harness.t, response, http.StatusOK, &page)
	require.Nil(harness.t, page.NextCursor, "harness grant helper requires one bounded page")
	return page.Items
}

func (harness *gatewayHarness) DeleteGrant(grantID string) {
	harness.t.Helper()
	response := harness.adminSnapshot(http.MethodDelete, "/api/v1/grants/"+url.PathEscape(grantID), nil)
	decodeSnapshot(harness.t, response, http.StatusNoContent, nil)
	require.Empty(harness.t, response.Body)
}

func (harness *gatewayHarness) SetupCurrentCatalog(namespace string, tools []fixtureTool) currentCatalogHandle {
	harness.t.Helper()
	fixture := newRawHTTPFixtureWithTools(harness.t, "modern", tools)
	request := map[string]any{
		"namespace": namespace, "display_name": "E2E " + namespace, "enabled": true,
		"transport": map[string]any{"kind": "streamable_http", "url": fixture.URL(), "protocol_mode": "modern", "authentication": map[string]string{"mode": "none"}},
	}
	contents, err := json.Marshal(request)
	require.NoError(harness.t, err)
	var creation stdioCreation
	response := harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v1/servers", contents, map[string]string{"Idempotency-Key": "catalog-" + namespace})
	decodeSnapshot(harness.t, response, http.StatusCreated, &creation)
	etag := response.Header.Get("ETag")
	require.NotNil(harness.t, creation.Operation)
	harness.WaitOperation(creation.Server.ID, creation.Operation.ID, contract.OperationSucceeded)
	waitForStdioServer(harness.t, harness, creation.Server.ID, func(server stdioServerView) bool {
		return server.Runtime.State == contract.RuntimeActive && server.Catalog.ActiveState == contract.ActiveCatalogCurrent && server.Catalog.ActiveToolCount == int64(len(tools))
	})
	return currentCatalogHandle{ServerID: creation.Server.ID, ETag: etag, Namespace: namespace, Fixture: fixture}
}

func (harness *gatewayHarness) ModernRequest(bearer *agentBearer, body []byte) responseSnapshot {
	harness.t.Helper()
	return harness.requestSnapshot(http.MethodPost, "/mcp", body, map[string]string{
		"Authorization": bearer.authorizationHeader(), "Content-Type": contract.MediaTypeJSON, "Accept": contract.MediaTypeJSON + ", " + contract.MediaTypeEventStream,
		"Mcp-Protocol-Version": contract.ModernProtocolVersion,
	})
}

func (harness *gatewayHarness) ModernDiscover(bearer *agentBearer, id json.RawMessage) responseSnapshot {
	harness.t.Helper()
	return harness.ModernRequest(bearer, rawRPCBody(harness.t, id, "server/discover", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"e2e-harness","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}`))
}

func (harness *gatewayHarness) ModernList(bearer *agentBearer, id json.RawMessage, cursor string) responseSnapshot {
	harness.t.Helper()
	params := `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"e2e-harness","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}`
	if cursor != "" {
		encoded, err := json.Marshal(cursor)
		require.NoError(harness.t, err)
		params = `{"cursor":` + string(encoded) + `,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"e2e-harness","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}`
	}
	return harness.ModernRequest(bearer, rawRPCBody(harness.t, id, "tools/list", params))
}

func (harness *gatewayHarness) ModernCall(bearer *agentBearer, id json.RawMessage, name string, arguments json.RawMessage) responseSnapshot {
	harness.t.Helper()
	params := callParams(harness.t, name, arguments, `,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"e2e-harness","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`)
	return harness.ModernRequest(bearer, rawRPCBody(harness.t, id, "tools/call", params))
}

func (harness *gatewayHarness) LegacyInitialize(bearer *agentBearer, id json.RawMessage) (legacySessionHandle, responseSnapshot) {
	harness.t.Helper()
	body := rawRPCBody(harness.t, id, "initialize", `{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"e2e-harness","version":"1"}}`)
	response := harness.legacyRequest(http.MethodPost, bearer, legacySessionHandle{}, body)
	session := legacySessionHandle{ID: response.Header.Get("Mcp-Session-Id")}
	require.NotEmpty(harness.t, session.ID)
	initialized := harness.legacyRequest(http.MethodPost, bearer, session, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	require.Equal(harness.t, http.StatusAccepted, initialized.StatusCode, string(initialized.Body))
	require.Empty(harness.t, initialized.Body)
	return session, response
}

func (harness *gatewayHarness) LegacyList(bearer *agentBearer, session legacySessionHandle, id json.RawMessage, cursor string) responseSnapshot {
	harness.t.Helper()
	params := ""
	if cursor != "" {
		encoded, err := json.Marshal(cursor)
		require.NoError(harness.t, err)
		params = `{"cursor":` + string(encoded) + `}`
	}
	return harness.legacyRequest(http.MethodPost, bearer, session, rawRPCBody(harness.t, id, "tools/list", params))
}

func (harness *gatewayHarness) LegacyCall(bearer *agentBearer, session legacySessionHandle, id json.RawMessage, name string, arguments json.RawMessage) responseSnapshot {
	harness.t.Helper()
	return harness.legacyRequest(http.MethodPost, bearer, session, rawRPCBody(harness.t, id, "tools/call", callParams(harness.t, name, arguments, "")))
}

func (harness *gatewayHarness) LegacyDelete(bearer *agentBearer, session legacySessionHandle) responseSnapshot {
	harness.t.Helper()
	return harness.legacyRequest(http.MethodDelete, bearer, session, nil)
}

func (harness *gatewayHarness) legacyRequest(method string, bearer *agentBearer, session legacySessionHandle, body []byte) responseSnapshot {
	harness.t.Helper()
	headers := map[string]string{"Authorization": bearer.authorizationHeader()}
	if len(body) > 0 {
		headers["Content-Type"] = contract.MediaTypeJSON
		headers["Accept"] = contract.MediaTypeJSON + ", " + contract.MediaTypeEventStream
	}
	if session.ID != "" {
		headers["Mcp-Session-Id"] = session.ID
		headers["Mcp-Protocol-Version"] = contract.LegacyProtocolVersion
	}
	return harness.requestSnapshot(method, "/mcp", body, headers)
}

func (harness *gatewayHarness) AuditObservations() []auditObservation {
	harness.t.Helper()
	require.Nil(harness.t, harness.process, "audit inspection requires a stopped Gateway")
	ownership, err := gatewaypaths.Acquire(harness.root)
	require.NoError(harness.t, err)
	store, err := storage.Open(harness.ctx, ownership)
	require.NoError(harness.t, err)
	var observations []auditObservation
	err = store.View(harness.ctx, func(transaction *sql.Tx) error {
		observations, err = readAuditObservations(harness.ctx, transaction)
		return err
	})
	require.NoError(harness.t, err)
	require.NoError(harness.t, store.Close())
	require.NoError(harness.t, ownership.MarkClean())
	require.NoError(harness.t, ownership.Close())
	return observations
}

func (harness *gatewayHarness) ArtifactAuditObservations() []auditObservation {
	harness.t.Helper()
	require.Nil(harness.t, harness.process, "artifact inspection requires a stopped Gateway")
	return harness.readOnlyAuditObservations()
}

func (harness *gatewayHarness) LiveAuditObservations() []auditObservation {
	harness.t.Helper()
	require.NotNil(harness.t, harness.process, "live audit inspection requires a running Gateway")
	return harness.readOnlyAuditObservations()
}

func (harness *gatewayHarness) readOnlyAuditObservations() []auditObservation {
	harness.t.Helper()
	databasePath := filepath.Join(harness.root, gatewaypaths.DatabaseName)
	require.NoError(harness.t, gatewaypaths.ValidateOwnerOnlyFile(databasePath))
	databaseURL := url.URL{Scheme: "file", Path: databasePath}
	query := databaseURL.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "busy_timeout(1000)")
	databaseURL.RawQuery = query.Encode()
	database, err := sql.Open("sqlite3", databaseURL.String())
	require.NoError(harness.t, err)
	database.SetMaxOpenConns(1)
	harness.t.Cleanup(func() { require.NoError(harness.t, database.Close()) })
	observations, err := readAuditObservations(harness.ctx, database)
	require.NoError(harness.t, err)
	require.NoError(harness.t, database.Close())
	return observations
}

type auditQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readAuditObservations(ctx context.Context, queryer auditQueryer) ([]auditObservation, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT insertion_sequence, id, admission_class, decision, terminal_class FROM invocations ORDER BY insertion_sequence LIMIT 65537`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	observations := make([]auditObservation, 0)
	for rows.Next() {
		var observation auditObservation
		var admission string
		var decision, terminal sql.NullString
		if err := rows.Scan(&observation.Sequence, &observation.InvocationID, &admission, &decision, &terminal); err != nil {
			return nil, err
		}
		observation.AdmissionClass = contract.InvocationAdmissionClass(admission)
		if decision.Valid {
			observation.Decision = contract.AuthorizationDecision(decision.String)
		}
		if terminal.Valid {
			observation.TerminalClass = contract.InvocationTerminalClass(terminal.String)
		}
		observations = append(observations, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(observations) > 65536 {
		return nil, fmt.Errorf("audit observations exceed schema bound")
	}
	return observations, nil
}

func callParams(t *testing.T, name string, arguments json.RawMessage, suffix string) string {
	t.Helper()
	require.True(t, json.Valid(arguments), "invalid raw call arguments")
	encodedName, err := json.Marshal(name)
	require.NoError(t, err)
	params := `{"name":` + string(encodedName) + `,"arguments":` + string(arguments) + suffix + `}`
	require.True(t, json.Valid([]byte(params)), "invalid raw call params")
	return params
}

func rawRPCBody(t *testing.T, id json.RawMessage, method, params string) []byte {
	t.Helper()
	require.True(t, json.Valid(id), "invalid raw JSON-RPC ID")
	body := `{"jsonrpc":"2.0","id":` + string(id) + `,"method":` + fmt.Sprintf("%q", method)
	if params != "" {
		body += `,"params":` + params
	}
	body += `}`
	contents := []byte(body)
	require.True(t, json.Valid(contents), "invalid raw JSON-RPC body")
	return contents
}
