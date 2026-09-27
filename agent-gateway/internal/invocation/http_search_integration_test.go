//go:build integration

package invocation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestHTTPTrafficAuthoritativeSearchIntegration(t *testing.T) {
	_, audits, _, _, _ := newAdmissionCoordinator(t, nil)
	traffic, _ := trafficFixture(t, nil, nil)
	audits.traffic = traffic
	agent, other, missing := invocationID(80), invocationID(81), invocationID(82)
	names := &readNames{names: map[string]string{agent: "Café Investigator", other: agent + " unrelated"}}
	reader, err := NewReadService(audits, names)
	require.NoError(t, err)
	for n := 1; n <= 55; n++ {
		admission := httpTrafficAdmission(n)
		admission.Principal.ID = other
		if n <= 3 {
			admission.Principal.ID = agent
			admission.Target.Host = "api.github.com"
		}
		if n == 3 {
			admission.Principal.ID = missing
		}
		admission.Decision.Principal = admission.Principal
		if n <= 2 {
			admission.Connect = &contract.HTTPConnectContext{ID: invocationID(90), Host: "api.github.com", Port: 443}
		}
		receipt, admitErr := traffic.AdmitHTTP(t.Context(), admission)
		require.NoError(t, admitErr)
		traffic.Release(receipt)
	}
	page, err := reader.ListHTTP(t.Context(), contract.HTTPTrafficQuery{Limit: 50})
	require.NoError(t, err)
	require.Len(t, page.Items, 50)
	for _, row := range page.Items {
		require.NotEqual(t, agent, row.PrincipalID)
	}
	for _, name := range []string{"CAFE", "invest", "investigtaor", "cafe investigator"} {
		query := contract.HTTPTrafficQuery{Limit: 1, Filters: contract.HTTPTrafficFilters{Principal: name, SearchLocale: "en-US", Destination: "GiTHuB", ConnectID: invocationID(90), Type: "request", Decision: "allow", Outcome: "outcome_unknown"}}
		page, err = reader.ListHTTP(t.Context(), query)
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		require.Equal(t, invocationID(2), page.Items[0].ID)
		require.NotNil(t, page.NextCursor)
		require.LessOrEqual(t, len(*page.NextCursor), 512)
		query.Cursor = *page.NextCursor
		older, readErr := reader.ListHTTP(t.Context(), query)
		require.NoError(t, readErr)
		require.Len(t, older.Items, 1)
		require.Equal(t, invocationID(1), older.Items[0].ID)
		require.Nil(t, older.NextCursor)
		query.Filters.Destination = "example"
		_, readErr = reader.ListHTTP(t.Context(), query)
		require.ErrorIs(t, readErr, ErrStaleCursor)
	}
	for _, host := range []string{"%", "_", "*", "github.*", "github.com/path", "https://api.github.com", "' OR 1=1 --", "GITHBU"} {
		page, err = reader.ListHTTP(t.Context(), contract.HTTPTrafficQuery{Limit: 100, Filters: contract.HTTPTrafficFilters{Destination: host}})
		require.NoError(t, err)
		require.Empty(t, page.Items, host)
	}
	query := contract.HTTPTrafficQuery{Limit: 1, Filters: contract.HTTPTrafficFilters{Principal: "cafe"}}
	page, err = reader.ListHTTP(t.Context(), query)
	require.NoError(t, err)
	require.NotNil(t, page.NextCursor)
	query.Cursor = *page.NextCursor
	names.names[agent] = "Renamed investigator"
	_, err = reader.ListHTTP(t.Context(), query)
	require.ErrorIs(t, err, ErrStaleCursor)
	query.Cursor = ""
	page, err = reader.ListHTTP(t.Context(), query)
	require.NoError(t, err)
	require.Empty(t, page.Items, "old names are not retained evidence")
	names.err = errors.New("names unavailable")
	_, err = reader.ListHTTP(t.Context(), query)
	require.ErrorIs(t, err, ErrStorageUnavailable)
	// Exact diagnostic IDs do not consult recognition names or match another agent's name.
	query.Filters = contract.HTTPTrafficFilters{PrincipalID: agent}
	query.Limit = 100
	page, err = reader.ListHTTP(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	for _, row := range page.Items {
		require.Equal(t, agent, row.PrincipalID)
	}
	names.err = nil
	query.Filters = contract.HTTPTrafficFilters{Principal: missing}
	page, err = reader.ListHTTP(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, missing, page.Items[0].PrincipalID, "missing names do not erase literal identity matches")
	query.Filters = contract.HTTPTrafficFilters{Principal: "unrelated", PrincipalID: agent}
	page, err = reader.ListHTTP(t.Context(), query)
	require.NoError(t, err)
	require.Empty(t, page.Items, "recognition and diagnostic filters intersect")
	for _, bad := range []contract.HTTPTrafficFilters{{Principal: strings.Repeat("a", 257)}, {Destination: strings.Repeat("a", 257)}, {Principal: "a\n"}, {Destination: "a\u200b"}, {SearchLocale: "not_a_locale"}, {PrincipalID: "cafe"}} {
		_, err = reader.ListHTTP(t.Context(), contract.HTTPTrafficQuery{Limit: 50, Filters: bad})
		require.ErrorIs(t, err, ErrInvalidInput)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = reader.ListHTTP(ctx, contract.HTTPTrafficQuery{Limit: 50, Filters: contract.HTTPTrafficFilters{Destination: "github"}})
	require.Error(t, err)
}
