package invocation

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

func validGitTrafficFilters(f contract.GitTrafficFilters) bool {
	return validSearchText(f.Repository) && validSearchLocale(f.SearchLocale) &&
		slices.Contains([]string{"", "read_discovery", "read", "push_discovery", "probe", "push", "invalid"}, f.Operation) &&
		slices.Contains([]string{"", "allowed", "blocked"}, f.Admission) &&
		slices.Contains([]string{"", "not_dispatched", "unknown", "prestart_failure", "complete", "incomplete"}, f.Transport) &&
		slices.Contains([]string{"", "not_a_push", "unknown", "reported_success", "reported_failure", "reported_partial"}, f.Report)
}

// These predicates project recorded evidence only, matching the browser's facts.
const gitTransportPredicate = `CASE WHEN json_extract(admission,'$.allowed')=0 THEN 'not_dispatched' WHEN completion IS NULL THEN 'unknown' WHEN json_extract(completion,'$.outcome')='prestart_failure' THEN 'prestart_failure' WHEN json_extract(completion,'$.transfer_complete')=1 THEN 'complete' ELSE 'incomplete' END`
const gitReportPredicate = `CASE WHEN json_extract(admission,'$.operation')!='push' THEN 'not_a_push' ELSE coalesce(json_extract(completion,'$.reported_result'),'unknown') END`

// Scan only small recorded identities, stopping after the page's lookahead match.
// Retention bounds the scan; only one bounded selected page is hydrated by ListGit.
func selectGitTraffic(ctx context.Context, tx *sql.Tx, q contract.GitTrafficQuery, cursor invocationCursor) ([]int64, error) {
	clauses := []string{"insertion_sequence<=?", "(?=0 OR insertion_sequence<?)"}
	args := []any{cursor.UpperSequence, cursor.NextSequence, cursor.NextSequence}
	for _, f := range []struct{ expression, value string }{
		{"json_extract(admission,'$.operation')", q.Filters.Operation},
		{"CASE WHEN json_extract(admission,'$.allowed')=1 THEN 'allowed' ELSE 'blocked' END", q.Filters.Admission},
		{gitTransportPredicate, q.Filters.Transport}, {gitReportPredicate, q.Filters.Report},
	} {
		if f.value != "" {
			clauses = append(clauses, f.expression+"=?")
			args = append(args, f.value)
		}
	}
	//nolint:gosec // Predicate expressions are fixed above and all values are bound.
	rows, err := tx.QueryContext(ctx, gitTrafficSearchSelect+` WHERE `+strings.Join(clauses, " AND ")+` ORDER BY insertion_sequence DESC LIMIT 65537`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	search := newHistorySearch(q.Filters.Repository, q.Filters.SearchLocale)
	selected := []int64{}
	scanned := 0
	for rows.Next() {
		scanned++
		if scanned > 65536 {
			return nil, ErrInvalidState
		}
		var sequence int64
		var id, name string
		if err := rows.Scan(&sequence, &id, &name); err != nil {
			return nil, err
		}
		if len(search.tokens) != 0 && !strings.Contains(id, strings.TrimSpace(q.Filters.Repository)) && !search.matches(name) {
			continue
		}
		selected = append(selected, sequence)
		if len(selected) == q.Limit+1 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return selected, rows.Close()
}
