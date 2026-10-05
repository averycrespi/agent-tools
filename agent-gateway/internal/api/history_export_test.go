package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpboundary"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/stretchr/testify/require"
)

type exportFixture struct {
	calls int
	err   error
}

func (f *exportFixture) ExportHistory(_ context.Context, _ int64, _ int) (contract.HistoryExport, error) {
	f.calls++
	return contract.HistoryExport{Records: []contract.HistoryExportRecord{}}, f.err
}

func TestHistoryExportRouteIsAuthenticatedBoundedAndHistorySpecific(t *testing.T) {
	reader := new(exportFixture)
	apiHandler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, HistoryExport: reader})
	handler, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: apiHandler.Authenticate, Next: apiHandler})
	require.NoError(t, err)
	headers := map[string]string{"Authorization": "Bearer " + testBearer}
	require.Equal(t, 401, perform(handler, http.MethodGet, "/api/v2/history/export", "", nil).Code)
	response := perform(handler, http.MethodGet, "/api/v2/history/export?after_sequence=0&limit=256", "", headers)
	require.Equal(t, 200, response.Code)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	for _, query := range []string{"limit=257", "limit=0", "limit=01", "after_sequence=-1", "after_sequence=01", "after_sequence=9223372036854775808", "limit=1&limit=2", "unknown=value", "after_sequence="} {
		require.Equal(t, 400, perform(handler, http.MethodGet, "/api/v2/history/export?"+query, "", headers).Code)
	}
	require.Equal(t, 400, perform(handler, http.MethodGet, "/api/v2/history/export", "{}", headers).Code)
	require.Equal(t, 1, reader.calls)
	reader.err = invocation.ErrTrafficFault
	response = perform(handler, http.MethodGet, "/api/v2/history/export", "", headers)
	require.Equal(t, 503, response.Code)
	require.Contains(t, response.Body.String(), "history_unavailable")
	reader.err = invocation.ErrTrafficCapacity
	response = perform(handler, http.MethodGet, "/api/v2/history/export", "", headers)
	require.Equal(t, 503, response.Code)
	require.Contains(t, response.Body.String(), "history_busy")
}
