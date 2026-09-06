package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthenticatedRejectionAuditIsAtomicAndSecretFree(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "recorded", true: "audit_refused"}[refuse], func(t *testing.T) {
			ownership, err := gatewaypaths.Acquire(filepath.Join(t.TempDir(), "gateway"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, ownership.Close()) })
			store, err := storage.Initialize(t.Context(), ownership, testID)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			repository, err := audit.NewRepository(store)
			require.NoError(t, err)
			if refuse {
				require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
					_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER refuse_rejection BEFORE INSERT ON control_audit_events WHEN json_extract(NEW.event, '$.phase') = 'outcome' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
					return err
				}))
			}
			handler := New(Options{InstallationID: testID, Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Audit: repository})
			draining := false
			boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, AuthenticatedProblem: handler.RecordAuthenticatedProblem, Draining: func() bool { return draining }, Next: handler})
			require.NoError(t, err)
			for _, headers := range []map[string]string{
				{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON},
				{"Cookie": contract.SessionCookieName + "=session", "Origin": contract.CanonicalOrigin, "X-CSRF-Token": "csrf", "Content-Type": contract.MediaTypeJSON},
			} {
				response := perform(boundary, http.MethodPost, "/api/v1/admin-credentials", `{"rejection-secret-canary":`, headers)
				expected := contract.ProblemInvalidJSON
				if refuse {
					expected = contract.ProblemStorageUnavailable
				}
				problem, _ := contract.ProblemForCode(expected)
				assert.Equal(t, problem.Status, response.Code)
				expectedBody, err := json.Marshal(contract.ProblemEnvelope(problem))
				require.NoError(t, err)
				assert.JSONEq(t, string(expectedBody), response.Body.String())
			}
			page, err := repository.List(t.Context(), audit.Query{Limit: 100, Filters: contract.AuditFilters{Category: "admin_credential"}})
			require.NoError(t, err)
			if refuse {
				assert.Empty(t, page.Items)
			} else {
				require.Len(t, page.Items, 4)
				for index, summary := range page.Items {
					assert.Equal(t, contract.AuditOperator, summary.Actor.Type)
					assert.Equal(t, credential().ID, summary.Actor.Credential.ID)
					assert.Equal(t, credential().Fingerprint, summary.Actor.Credential.Fingerprint)
					item, err := repository.Read(t.Context(), summary.ID, page.History.Generation)
					require.NoError(t, err)
					serialized, err := json.Marshal(item.Event)
					require.NoError(t, err)
					assert.NotContains(t, string(serialized), "rejection-secret-canary")
					assert.NotContains(t, string(serialized), testBearer)
					if index%2 == 0 {
						assert.Equal(t, "rejected", summary.Outcome)
						assert.Equal(t, summary.CorrelationID, page.Items[index+1].CorrelationID)
						require.NotNil(t, item.Event.Detail.Problem)
						assert.Equal(t, string(contract.ProblemInvalidJSON), *item.Event.Detail.Problem)
					}
				}
			}
			assert.Equal(t, http.StatusUnauthorized, perform(boundary, http.MethodPost, "/api/v1/admin-credentials", "{", nil).Code)
			assert.Equal(t, http.StatusOK, perform(boundary, http.MethodGet, "/api/v1/system-status", "", map[string]string{"Authorization": "Bearer " + testBearer}).Code)
			after, err := repository.List(t.Context(), audit.Query{Limit: 100, Filters: contract.AuditFilters{Category: "admin_credential"}})
			require.NoError(t, err)
			assert.Equal(t, page, after)
			draining = true
			response := perform(boundary, http.MethodPost, "/api/v1/admin-credentials", "{", map[string]string{"Authorization": "Bearer " + testBearer})
			assert.Equal(t, http.StatusServiceUnavailable, response.Code)
			after, err = repository.List(t.Context(), audit.Query{Limit: 100, Filters: contract.AuditFilters{Category: "admin_credential"}})
			require.NoError(t, err)
			if refuse {
				assert.Empty(t, after.Items)
			} else {
				require.Len(t, after.Items, 6)
				assert.Equal(t, "unknown", after.Items[0].Outcome)
			}
		})
	}
}

func TestAuditReadAPIUsesAuthenticatedBoundedHistory(t *testing.T) {
	ownership, err := gatewaypaths.Acquire(filepath.Join(t.TempDir(), "gateway"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ownership.Close()) })
	store, err := storage.Initialize(t.Context(), ownership, testID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	repository, err := audit.NewRepository(store)
	require.NoError(t, err)
	entry, err := repository.Append(t.Context(), contract.AuditEvent{AuditSummary: contract.AuditSummary{
		ID: testID, Timestamp: "2026-09-05T00:00:00.000000000Z", Category: "grant", Action: "create", Phase: "outcome", Outcome: "succeeded",
		Actor: contract.AuditActor{Type: contract.AuditSystem}, CorrelationID: testID, Target: contract.AuditTarget{Type: "grant", ID: testID},
	}})
	require.NoError(t, err)
	handler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Audit: repository})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	bearer := map[string]string{"Authorization": "Bearer " + testBearer}
	response := perform(boundary, http.MethodGet, "/api/v1/audit-events?limit=1&actor_type=system&category=grant&action=create", "", bearer)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	assert.Empty(t, response.Header().Get("Access-Control-Allow-Origin"))
	assert.NotContains(t, response.Body.String(), `"detail"`)
	var page contract.AuditPage
	require.NoError(t, strictjson.Decode(response.Body.Bytes(), &page, strictjson.Options{MaxBytes: 8192, MaxDepth: 8, RejectUnknownMembers: true}))
	require.Len(t, page.Items, 1)
	assert.Equal(t, entry.AuditSummary, page.Items[0])
	assert.NotNil(t, page.History.OldestRetained)
	assert.False(t, page.History.Pruned)

	response = perform(boundary, http.MethodGet, "/api/v1/audit-events/"+testID+"?generation="+page.History.Generation, "", bearer)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var item contract.AuditItem
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &item))
	assert.Equal(t, entry, item.Event)
	assert.Equal(t, page.History, item.History)
	assert.Contains(t, response.Body.String(), `"detail":{"reason":null,"problem":null}`)
	for _, suffix := range []string{"", "/" + testID} {
		response = perform(boundary, http.MethodGet, "/api/v1/audit-events"+suffix+"?generation="+strings.Repeat("0", 64), "", bearer)
		assert.Equal(t, http.StatusConflict, response.Code)
		assert.Contains(t, response.Body.String(), "audit_history_replaced")
		assert.Equal(t, http.StatusUnauthorized, perform(boundary, http.MethodGet, "/api/v1/audit-events"+suffix, "", nil).Code)
		assert.Equal(t, http.StatusForbidden, perform(boundary, http.MethodGet, "/api/v1/audit-events"+suffix, "", map[string]string{"Cookie": contract.SessionCookieName + "=session"}).Code)
		assert.Equal(t, http.StatusOK, perform(boundary, http.MethodGet, "/api/v1/audit-events"+suffix, "", map[string]string{"Cookie": contract.SessionCookieName + "=session", "Origin": contract.CanonicalOrigin}).Code)
	}
	for _, query := range []string{"unknown=x", "limit=1&limit=2", "limit=0", "limit=101", "limit=01", "cursor=", "cursor=garbage", "actor_type=human", "credential_id=canary", "category=invocation", "category=grant_request&action=submit", "target_type=url", "target_id=bad", "outcome=other", "correlation_id=bad", "from=2026-09-05T00:00:00.000000000Z", "generation=bad", "generation="} {
		response = perform(boundary, http.MethodGet, "/api/v1/audit-events?"+query, "", bearer)
		assert.Equal(t, http.StatusBadRequest, response.Code, query)
	}
	assert.Equal(t, http.StatusBadRequest, perform(boundary, http.MethodGet, "/api/v1/audit-events", `{}`, bearer).Code)
	assert.Equal(t, http.StatusBadRequest, perform(boundary, http.MethodGet, "/api/v1/audit-events/"+testID+"?limit=1", "", bearer).Code)
	for _, method := range []string{http.MethodHead, http.MethodPost, http.MethodDelete, http.MethodPatch} {
		response = perform(boundary, method, "/api/v1/audit-events", "", bearer)
		assert.Equal(t, http.StatusMethodNotAllowed, response.Code)
		assert.Equal(t, http.MethodGet, response.Header().Get("Allow"))
	}
	after, err := repository.List(t.Context(), audit.Query{Limit: 100, Filters: contract.AuditFilters{Category: "grant", Action: "create", ActorType: contract.AuditSystem}})
	require.NoError(t, err)
	assert.Equal(t, page, after, "reads and unauthenticated failures must not write audit events")
}
