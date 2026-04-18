package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
)

// ---------- fakes for tunneled-hosts tests ----------

// fakeRulesLister implements RulesLister for tests.
type fakeRulesLister struct {
	ruleHosts []string
}

func (f *fakeRulesLister) Rules() []*rules.Rule { return nil }
func (f *fakeRulesLister) AllRuleHosts() []string {
	return f.ruleHosts
}

// fakeAuditor is a minimal audit.Logger for tests; only Query is used by
// handleTunneledHosts.
type fakeAuditor struct {
	entries []audit.Entry
}

func (f *fakeAuditor) Record(_ context.Context, _ audit.Entry) error { return nil }
func (f *fakeAuditor) Count(_ context.Context, _ audit.Filter) (int, error) {
	return len(f.entries), nil
}
func (f *fakeAuditor) Prune(_ context.Context, _ time.Time) (int, error) { return 0, nil }
func (f *fakeAuditor) Query(_ context.Context, _ audit.Filter) ([]audit.Entry, error) {
	return f.entries, nil
}

// ---------- TestStatsTunneledHosts ----------

func TestStatsTunneledHosts_EmptyWhenNoTunnelRows(t *testing.T) {
	srv, token := newTestServer(t, Deps{
		Auditor: &fakeAuditor{},
	})
	resp := authedGet(t, srv, token, "/dashboard/api/stats/tunneled-hosts?since=24h")
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	var out []tunneledHostEntry
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Empty(t, out)
}

func TestStatsTunneledHosts_ReturnsTunneledHostsWithoutRules(t *testing.T) {
	// Two tunnel rows for api.example.com (no rule), one for api.github.com (has rule).
	entries := []audit.Entry{
		{Host: "api.example.com", Interception: "tunnel"},
		{Host: "api.example.com", Interception: "tunnel"},
		{Host: "api.github.com", Interception: "tunnel"},
		{Host: "api.github.com", Interception: "mitm"}, // mitm row — should be ignored
	}
	srv, token := newTestServer(t, Deps{
		Auditor: &fakeAuditor{entries: entries},
		Rules:   &fakeRulesLister{ruleHosts: []string{"api.github.com"}},
	})
	resp := authedGet(t, srv, token, "/dashboard/api/stats/tunneled-hosts?since=24h")
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out []tunneledHostEntry
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))

	// Only api.example.com should be in the result (api.github.com is covered).
	require.Len(t, out, 1)
	require.Equal(t, "api.example.com", out[0].Host)
	require.Equal(t, 2, out[0].Count)
}

func TestStatsTunneledHosts_ExcludesHostsCoveredByRules(t *testing.T) {
	entries := []audit.Entry{
		{Host: "covered.example.com", Interception: "tunnel"},
		{Host: "uncovered.example.com", Interception: "tunnel"},
	}
	srv, token := newTestServer(t, Deps{
		Auditor: &fakeAuditor{entries: entries},
		Rules:   &fakeRulesLister{ruleHosts: []string{"covered.example.com"}},
	})
	resp := authedGet(t, srv, token, "/dashboard/api/stats/tunneled-hosts?since=24h")
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out []tunneledHostEntry
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))

	require.Len(t, out, 1)
	require.Equal(t, "uncovered.example.com", out[0].Host)
}

func TestStatsTunneledHosts_NilAuditorReturnsEmpty(t *testing.T) {
	srv, token := newTestServer(t, Deps{Auditor: nil})
	resp := authedGet(t, srv, token, "/dashboard/api/stats/tunneled-hosts?since=24h")
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "[]")
}
