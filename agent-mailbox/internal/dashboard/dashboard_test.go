package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-mailbox/internal/store"
	"github.com/stretchr/testify/require"
)

func TestIndexMatchesMailboxDashboardConventions(t *testing.T) {
	dash := New(testStore(t))
	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "Agent Mailbox")
	require.Contains(t, body, "--bg:#080b10")
	require.Contains(t, body, "--panel:#101722")
	require.Contains(t, body, "radial-gradient")
	require.Contains(t, body, "status-dot")
	require.Contains(t, body, "grid-template-columns:minmax(320px,430px) minmax(0,1fr)")
	require.Contains(t, body, "Needs response")
	require.Contains(t, body, "data-quick=\"new\"")
	require.Contains(t, body, "data-quick=\"all\"")
	require.Contains(t, body, "Ack all")
	require.Contains(t, body, "Resolve all")
	require.Contains(t, body, "Refresh")
	require.Contains(t, body, "Reset")
	require.Contains(t, body, "$('ack-all').onclick")
	require.Contains(t, body, "$('resolve-all').onclick")
	require.Contains(t, body, "$('refresh').onclick")
	require.Contains(t, body, "$('reset').onclick")
	require.Contains(t, body, "confirm(`${label.charAt(0).toUpperCase() + label.slice(1)}")
	require.Contains(t, body, "updateQuickFilters")
	require.Contains(t, body, "$('ack').disabled = m.status === 'acknowledged' || m.status === 'resolved'")
	require.Contains(t, body, "$('resolve').disabled = m.status === 'resolved'")
	require.Contains(t, body, "min-height: 44vh")
	require.Contains(t, body, "id:")
	require.Contains(t, body, "created-at:")
	require.Contains(t, body, "updated-at:")
	require.Contains(t, body, "overflow-wrap: anywhere")
	require.NotContains(t, body, "<details class=\"detail-section\">")
	require.NotContains(t, body, "Lifecycle events")
	require.Contains(t, body, "Sender")
	require.Contains(t, body, "'sender'")
	require.Contains(t, body, "new EventSource('events')")
	require.Contains(t, body, "refresh().catch(console.error)")
	require.NotContains(t, body, "JSON.parse(e.data)")
	require.NotContains(t, body, "Search messages")
	require.NotContains(t, body, "id=\"search\"")
}

func TestAPIMessagesFiltersDetailsAndLifecycle(t *testing.T) {
	st := testStore(t)
	msg, _, err := st.SendMessage(context.Background(), store.SendMessageParams{Sender: "agent", Subject: "Hello", Body: "Body", Channel: "ops", Severity: store.SeverityWarning, RequiresResponse: true})
	require.NoError(t, err)
	dash := New(st)

	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/messages?channel=ops&sender=agent&severity=warning&requires_response=true", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var list store.ListMessagesResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Messages, 1)
	require.Empty(t, list.Messages[0].Body)

	rec = httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/messages/"+msg.ID, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var detail store.MessageDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	require.Equal(t, "Body", detail.Message.Body)

	rec = httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/messages/"+msg.ID+"/ack?actor=avery", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "acknowledged")

	rec = httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/messages/"+msg.ID+"/resolve?resolution=done", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "resolved")
}

func TestAPIValidationErrors(t *testing.T) {
	dash := New(testStore(t))
	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/messages?limit=bad", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, `{"error":"limit must be an integer"}`, rec.Body.String())
}

func TestSSESendsSnapshot(t *testing.T) {
	st := testStore(t)
	_, _, err := st.SendMessage(context.Background(), store.SendMessageParams{Sender: "agent", Subject: "Hello", Body: "Body"})
	require.NoError(t, err)
	dash := New(st)
	dash.pollInterval = time.Hour
	server := httptest.NewServer(dash.Handler())
	defer server.Close()

	resp, err := server.Client().Get(server.URL + "/events")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	scanner := bufio.NewScanner(resp.Body)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) >= 2 {
			break
		}
	}
	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, "event: snapshot")
	require.Contains(t, joined, "\"total\":1")
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "mailbox.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return st
}
