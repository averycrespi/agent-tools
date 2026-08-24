package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCatalogService struct {
	status catalog.DurableStatus
	page   catalog.DescriptorPage
	item   contract.ToolDescriptor
	err    error
	filter contract.DescriptorRetiredFilter
	cursor *catalog.DescriptorCursor
	limit  int
	server string
	toolID string
}

func (service *fakeCatalogService) Status(context.Context, string) (catalog.DurableStatus, error) {
	return service.status, service.err
}
func (service *fakeCatalogService) GetDescriptor(_ context.Context, serverID, toolID string) (contract.ToolDescriptor, error) {
	service.server, service.toolID = serverID, toolID
	return service.item, service.err
}
func (service *fakeCatalogService) ListDescriptors(_ context.Context, serverID string, filter contract.DescriptorRetiredFilter, cursor *catalog.DescriptorCursor, limit int) (catalog.DescriptorPage, error) {
	service.server, service.filter, service.cursor, service.limit = serverID, filter, cursor, limit
	return service.page, service.err
}

func TestDescriptorListAndMemberResources(t *testing.T) {
	item := descriptorResource()
	next := catalog.DescriptorCursor{ServerID: testID, Retired: contract.DescriptorRetiredInclude, CatalogRevision: "1", Upper: 2, After: 1, AfterID: item.ID}
	service := &fakeCatalogService{page: catalog.DescriptorPage{Items: []catalog.DescriptorRecord{{InsertionSequence: 1, Resource: item}}, Next: &next}, item: item}
	handler := newCatalogTestHandler(t, service)
	listed := perform(handler, http.MethodGet, "/api/v1/servers/"+testID+"/descriptors?limit=1", "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	assert.Contains(t, listed.Body.String(), `"upstream_name":"echo"`)
	assert.Contains(t, listed.Body.String(), `"next_cursor":"`)
	assert.Equal(t, contract.DescriptorRetiredInclude, service.filter)
	assert.Equal(t, 1, service.limit)

	cursor := encodeDescriptorCursor(next)
	second := perform(handler, http.MethodGet, "/api/v1/servers/"+testID+"/descriptors?limit=2&retired=only&cursor="+cursor, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	assert.Equal(t, contract.DescriptorRetiredOnly, service.filter)
	require.NotNil(t, service.cursor)
	assert.Equal(t, next.AfterID, service.cursor.AfterID)

	member := perform(handler, http.MethodGet, "/api/v1/servers/"+testID+"/descriptors/"+item.ID, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, member.Code, member.Body.String())
	assert.Empty(t, member.Header().Get("ETag"))
	assert.Equal(t, item.ID, service.toolID)
}

func TestDescriptorRequestValidationAndSafeErrors(t *testing.T) {
	service := &fakeCatalogService{item: descriptorResource()}
	handler := newCatalogTestHandler(t, service)
	for _, target := range []string{
		"/api/v1/servers/" + testID + "/descriptors?unknown=1",
		"/api/v1/servers/" + testID + "/descriptors?limit=0",
		"/api/v1/servers/" + testID + "/descriptors?retired=bad",
		"/api/v1/servers/" + testID + "/descriptors?retired=include&retired=only",
	} {
		response := perform(handler, http.MethodGet, target, "", map[string]string{"Authorization": "Bearer " + testBearer})
		assert.Equal(t, http.StatusBadRequest, response.Code, target)
	}
	invalidCursor := perform(handler, http.MethodGet, "/api/v1/servers/"+testID+"/descriptors?cursor=not-base64!", "", map[string]string{"Authorization": "Bearer " + testBearer})
	assert.Equal(t, http.StatusBadRequest, invalidCursor.Code)
	assert.Contains(t, invalidCursor.Body.String(), "invalid_cursor")
	memberQuery := perform(handler, http.MethodGet, "/api/v1/servers/"+testID+"/descriptors/"+service.item.ID+"?x=1", "", map[string]string{"Authorization": "Bearer " + testBearer})
	assert.Equal(t, http.StatusBadRequest, memberQuery.Code)

	service.err = servers.ErrStaleCursor
	stale := perform(handler, http.MethodGet, "/api/v1/servers/"+testID+"/descriptors", "", map[string]string{"Authorization": "Bearer " + testBearer})
	assert.Equal(t, http.StatusConflict, stale.Code)
	assert.Contains(t, stale.Body.String(), "stale_cursor")
	service.err = servers.ErrNotFound
	missing := perform(handler, http.MethodGet, "/api/v1/servers/"+testID+"/descriptors/"+service.item.ID, "", map[string]string{"Authorization": "Bearer " + testBearer})
	assert.Equal(t, http.StatusNotFound, missing.Code)
	service.err = errors.New("private dependency details")
	failed := perform(handler, http.MethodGet, "/api/v1/servers/"+testID+"/descriptors/"+service.item.ID, "", map[string]string{"Authorization": "Bearer " + testBearer})
	assert.Equal(t, http.StatusServiceUnavailable, failed.Code)
	assert.NotContains(t, failed.Body.String(), "private")
}

func TestServerResourceComposesDurableCatalogWithoutActiveFacts(t *testing.T) {
	serverService := new(fakeServerService)
	serverService.server = storedServer(servers.Definition{Namespace: "alpha", DisplayName: "Alpha", Enabled: false, Transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}}})
	revision, success := "7", "2026-08-23T00:00:00Z"
	catalogService := &fakeCatalogService{status: catalog.DurableStatus{State: contract.DurableCatalogCurrent, Revision: &revision, ToolCount: 3, LastSuccessAt: &success}}
	handler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Servers: serverService, Catalog: catalogService})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	response := perform(boundary, http.MethodGet, "/api/v1/servers/"+testID, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), `"durable_state":"current","active_state":"absent","durable_revision":"7","active_revision":null,"durable_tool_count":3,"active_tool_count":0,"last_success_at":"2026-08-23T00:00:00Z"`)
}

func newCatalogTestHandler(t *testing.T, service CatalogService) http.Handler {
	t.Helper()
	handler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Catalog: service})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	return boundary
}

func descriptorResource() contract.ToolDescriptor {
	return contract.ToolDescriptor{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", ServerID: testID, UpstreamName: "echo", ExternalName: "alpha.echo", Descriptor: contract.NormalizedToolDescriptor{Name: "echo", InputSchema: []byte(`{"type":"object"}`), Annotations: contract.NormalizedToolAnnotations{DestructiveHint: true, OpenWorldHint: true}}, Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CatalogRevision: "1", FirstSeenAt: "2026-08-23T00:00:00Z", LastSeenAt: "2026-08-23T00:00:00Z"}
}
