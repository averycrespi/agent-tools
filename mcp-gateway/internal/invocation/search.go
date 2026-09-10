package invocation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
)

const invocationSearchSelect = `SELECT insertion_sequence, id, principal_id, requested_name, server_id, upstream_name FROM invocations`

func scanInvocationSearch(row invocationScanner) (contract.InvocationAuditRecord, error) {
	var record contract.InvocationAuditRecord
	err := row.Scan(&record.Sequence, &record.InvocationID, &record.PrincipalID, &record.RequestedName, &record.ServerID, &record.UpstreamName)
	return record, err
}

// Hydrate only the selected page and its continuation probe, in one bounded
// statement after closing the candidate scan. Captures never enter this path.
func hydrateInvocationSelection(ctx context.Context, tx *sql.Tx, selected []contract.InvocationAuditRecord) ([]contract.InvocationAuditRecord, error) {
	if len(selected) == 0 {
		return selected, nil
	}
	arguments := make([]any, len(selected))
	for index, record := range selected {
		arguments[index] = record.Sequence
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(selected)), ",")
	rows, err := tx.QueryContext(ctx, invocationSummarySelect+" WHERE insertion_sequence IN ("+placeholders+") ORDER BY insertion_sequence DESC", arguments...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]contract.InvocationAuditRecord, 0, len(selected))
	for rows.Next() {
		record, err := scanInvocation(rows)
		if err != nil {
			return nil, err
		}
		if !validStoredInvocation(record) {
			return nil, invalidInvocationState("invocation row is malformed")
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(selected) {
		return nil, invalidInvocationState("invocation selection changed inside read view")
	}
	return result, rows.Close()
}

// PrincipalDisplayNames keeps current-name SQL with its authorization owner.
type PrincipalDisplayNames interface {
	PrincipalDisplayNamesTx(context.Context, *sql.Tx) (map[string]string, error)
}

type ReadService struct {
	repository *Repository
	names      PrincipalDisplayNames
}

func NewReadService(repository *Repository, names PrincipalDisplayNames) (*ReadService, error) {
	if repository == nil || names == nil {
		return nil, errors.New("invocation read dependencies are incomplete")
	}
	return &ReadService{repository: repository, names: names}, nil
}

func (service *ReadService) List(ctx context.Context, query contract.InvocationListQuery) (contract.InvocationPage, error) {
	return service.repository.list(ctx, query, service.names)
}

func (service *ReadService) Get(ctx context.Context, id string) (contract.Invocation, error) {
	return service.repository.Get(ctx, id)
}

func searchDigest(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validSearchText(value string) bool {
	if len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

func validSearchLocale(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 64 {
		return false
	}
	tag, err := language.Parse(value)
	return err == nil && tag.String() == value
}

// Browser collection search uses UTF-16 edit distance, ASCII digits and
// accent-insensitive locale casing. Keep those rules rather than treating a
// display-name query as an exact identity selector.
type historySearch struct {
	tokens []string
	lower  cases.Caser
}

func newHistorySearch(query, locale string) historySearch {
	tag := language.Und
	if locale != "" {
		tag, _ = language.Parse(locale)
	}
	search := historySearch{lower: cases.Lower(tag)}
	search.tokens = strings.Fields(search.normalize(strings.TrimSpace(query)))
	return search
}
func (search historySearch) normalize(value string) string {
	decomposed := norm.NFKD.String(value)
	withoutMarks := strings.Map(func(r rune) rune {
		if unicode.IsMark(r) {
			return -1
		}
		return r
	}, decomposed)
	return search.lower.String(withoutMarks)
}
func (search historySearch) matches(value string) bool {
	if len(search.tokens) == 0 {
		return true
	}
	candidate := search.normalize(value)
	for _, token := range search.tokens {
		if strings.Contains(candidate, token) {
			continue
		}
		units := utf16.Encode([]rune(token))
		if len(units) < 4 || strings.ContainsAny(token, "0123456789") {
			return false
		}
		matched := false
		for _, word := range strings.FieldsFunc(candidate, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
			if historyWithinOneEdit(utf16.Encode([]rune(word)), units) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
func historyWithinOneEdit(left, right []uint16) bool {
	if len(left)-len(right) > 1 || len(right)-len(left) > 1 {
		return false
	}
	i, j, edits := 0, 0, 0
	for i < len(left) && j < len(right) {
		if left[i] == right[j] {
			i++
			j++
			continue
		}
		edits++
		if edits > 1 {
			return false
		}
		switch {
		case len(left) == len(right) && i+1 < len(left) && j+1 < len(right) && left[i] == right[j+1] && left[i+1] == right[j]:
			i += 2
			j += 2
		case len(left) > len(right):
			i++
		case len(right) > len(left):
			j++
		default:
			i++
			j++
		}
	}
	return edits+len(left)-i+len(right)-j <= 1
}
func invocationSearchLabel(record contract.InvocationAuditRecord) string {
	if record.RequestedName != nil {
		return *record.RequestedName
	}
	if record.UpstreamName == nil {
		return "Not resolved"
	}
	if record.ServerID != nil && *record.ServerID == contract.SyntheticServerID {
		return "mcp_gateway." + *record.UpstreamName
	}
	return *record.UpstreamName
}
