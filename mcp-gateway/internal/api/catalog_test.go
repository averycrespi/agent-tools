package api

import (
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const apiActiveProcessID = "01ARZ3NDEKTSV4RRFFQ69G5FAY"

type fakeActiveCatalog struct {
	page   catalog.ActivePage
	status catalog.ActiveStatus
	err    error
	cursor *catalog.ActiveCursor
	limit  int
}

func (service *fakeActiveCatalog) Status(string) catalog.ActiveStatus { return service.status }
func (service *fakeActiveCatalog) List(cursor *catalog.ActiveCursor, limit int) (catalog.ActivePage, error) {
	service.cursor, service.limit = cursor, limit
	return service.page, service.err
}
func (service *fakeActiveCatalog) Occupancy() contract.LimitStatus {
	return contract.LimitStatus{InUse: service.status.ToolCount, Limit: 2048}
}

func TestActiveCatalogResourceAndCursor(t *testing.T) {
	item := descriptorResource()
	next := catalog.ActiveCursor{Generation: apiActiveProcessID + "-1", Upper: 2, After: 1, AfterID: item.ID}
	changed := "2026-08-23T00:00:00Z"
	service := &fakeActiveCatalog{page: catalog.ActivePage{Summary: contract.CatalogSummary{ActiveState: contract.AggregateCatalogCurrent, ActiveGeneration: apiActiveProcessID + "-1", ChangedAt: &changed, IssueCount: 1}, Items: []catalog.DescriptorRecord{{InsertionSequence: 1, Resource: item}}, ServerDisplayNames: map[string]string{item.ServerID: "Display server"}, Next: &next}}
	handler := newActiveCatalogTestHandler(t, service)
	response := perform(handler, http.MethodGet, "/api/v1/catalog?limit=1", "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), `"active_state":"current"`)
	assert.Contains(t, response.Body.String(), `"active_generation":"`+apiActiveProcessID+`-1"`)
	assert.Contains(t, response.Body.String(), `"next_cursor":"`)
	assert.Contains(t, response.Body.String(), `"server_display_name":"Display server"`)
	assert.Equal(t, 1, service.limit)

	cursor := encodeActiveCatalogCursor(next)
	second := perform(handler, http.MethodGet, "/api/v1/catalog?cursor="+cursor, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.NotNil(t, service.cursor)
	assert.Equal(t, next.Generation, service.cursor.Generation)
}

func TestActiveCatalogRejectsInvalidQueriesAndStaleCursor(t *testing.T) {
	service := new(fakeActiveCatalog)
	handler := newActiveCatalogTestHandler(t, service)
	for _, target := range []string{"/api/v1/catalog?unknown=1", "/api/v1/catalog?limit=0", "/api/v1/catalog?limit=1&limit=2", "/api/v1/catalog?cursor=not-base64!"} {
		response := perform(handler, http.MethodGet, target, "", map[string]string{"Authorization": "Bearer " + testBearer})
		assert.Equal(t, http.StatusBadRequest, response.Code, target)
	}
	service.err = servers.ErrStaleCursor
	stale := perform(handler, http.MethodGet, "/api/v1/catalog", "", map[string]string{"Authorization": "Bearer " + testBearer})
	assert.Equal(t, http.StatusConflict, stale.Code)
	assert.Contains(t, stale.Body.String(), "stale_cursor")
}

func newActiveCatalogTestHandler(t *testing.T, service ActiveCatalogService) http.Handler {
	t.Helper()
	handler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, ActiveCatalog: service})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	return boundary
}
