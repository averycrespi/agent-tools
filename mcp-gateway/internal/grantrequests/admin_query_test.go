package grantrequests

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func (inspector *fakeNamespaceInspector) GrantDisplayNamesTx(context.Context, *sql.Tx) (map[string]string, error) {
	names := make(map[string]string)
	for _, target := range inspector.targets {
		names[target.ID] = "Named " + target.Namespace
	}
	return names, inspector.err
}

func TestAdminDecisionQueueSelectsCompleteBoundedSnapshot(t *testing.T) {
	fixture := newApprovalFixture(t)
	fixture.requests.principalNames = fixture.authority
	fixture.requests.entropy = bytes.NewReader(bytes.Repeat([]byte{0x44}, 2048))
	ids := make([]string, 0, 128)
	for index := range 128 {
		fixture.clock.now = requestTestTime.Add(time.Duration(index) * time.Second)
		duration := strconv.Itoa(60 + index)
		policy := serverApprovalPolicy()
		policy.DurationSeconds = &duration
		request := fixture.createRequest(t, policy)
		ids = append(ids, request.ID)
		if index%4 != 0 {
			_, err := fixture.requests.CancelOwned(context.Background(), fixture.principal.Principal.ID, request.ID)
			require.NoError(t, err)
		}
	}
	pending := contract.RequestPending
	filter := AdminFilter{State: &pending, Query: &AdminQuery{Sort: "submitted", Direction: "ascending"}}
	page, err := fixture.requests.ListAdmin(t.Context(), filter, nil, 50)
	require.NoError(t, err)
	require.Len(t, page.Table, 32)
	require.Equal(t, 32, page.TotalCount)
	require.Equal(t, ids[0], page.Table[0].Request.ID)
	require.Equal(t, ids[124], page.Table[31].Request.ID)
	require.Equal(t, "Request owner", page.Table[0].PrincipalDisplayName)
	require.Equal(t, "Named sample", page.Table[0].ServerDisplayName)
	require.Equal(t, requestID(400), page.Table[0].ResolvedServerID)
	require.Nil(t, page.Table[0].ResolvedUpstreamName)
	contents, err := json.Marshal(page.Table)
	require.NoError(t, err)
	require.NotContains(t, string(contents), "evidence")
	require.NotContains(t, string(contents), "descriptor")
	all := AdminFilter{Query: &AdminQuery{Sort: "submitted", Direction: "descending"}}
	first, err := fixture.requests.ListAdmin(t.Context(), all, nil, 50)
	require.NoError(t, err)
	require.Len(t, first.Table, 50)
	require.Equal(t, 128, first.TotalCount)
	require.Equal(t, ids[127], first.Table[0].Request.ID)
	second, err := fixture.requests.ListAdmin(t.Context(), all, first.Next, 50)
	require.NoError(t, err)
	require.Equal(t, 50, second.Offset)
	require.Len(t, second.Table, 50)
	third, err := fixture.requests.ListAdmin(t.Context(), all, second.Next, 50)
	require.NoError(t, err)
	require.Equal(t, 100, third.Offset)
	require.Len(t, third.Table, 28)
	require.Nil(t, third.Next)
	searched := AdminFilter{Query: &AdminQuery{Request: ids[0], Principal: "Request owner", Target: "Named sample", Scope: "server"}}
	match, err := fixture.requests.ListAdmin(t.Context(), searched, nil, 50)
	require.NoError(t, err)
	require.Equal(t, 1, match.TotalCount)
	require.Equal(t, ids[0], match.Table[0].Request.ID)
	_, err = fixture.requests.ListAdmin(t.Context(), searched, first.Next, 50)
	require.ErrorIs(t, err, ErrStaleCursor)
	tampered := *first.Next
	tampered.After++
	_, err = fixture.requests.ListAdmin(t.Context(), all, &tampered, 50)
	require.ErrorIs(t, err, ErrStaleCursor)
	_, err = fixture.requests.CancelOwned(t.Context(), fixture.principal.Principal.ID, ids[0])
	require.NoError(t, err)
	_, err = fixture.requests.ListAdmin(t.Context(), all, first.Next, 50)
	require.ErrorIs(t, err, ErrStaleCursor)
	fresh, err := fixture.requests.ListAdmin(t.Context(), filter, nil, 50)
	require.NoError(t, err)
	require.Equal(t, 31, fresh.TotalCount)
	require.Equal(t, ids[4], fresh.Table[0].Request.ID)
	legacy, err := fixture.requests.ListAdmin(t.Context(), AdminFilter{}, nil, 1)
	require.NoError(t, err)
	require.Equal(t, ids[0], legacy.Items[0].ID)
	require.Nil(t, legacy.Table)
	_, err = fixture.requests.ListAdmin(t.Context(), AdminFilter{}, first.Next, 50)
	require.ErrorIs(t, err, ErrStaleCursor)
}
