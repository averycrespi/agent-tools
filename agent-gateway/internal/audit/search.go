package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
)

// Search reads only bounded target identities and current names, never all event
// payloads. Domain owners resolve names in the same read transaction as selection.
func (repository *Repository) searchTargets(ctx context.Context, tx *sql.Tx, filters contract.AuditFilters, binding cursor) (map[contract.AuditTarget]bool, string, error) {
	binding.Before = 0
	statement, args := listStatement(filters, binding, contract.AuditRetention+1)
	statement = strings.Replace(statement, "SELECT insertion_sequence, event", "SELECT DISTINCT target_type, target_id", 1)
	// DISTINCT targets do not need an event-sequence order.
	statement = strings.Replace(statement, " ORDER BY insertion_sequence DESC", "", 1)
	rows, err := tx.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, "", err
	}
	targets := []contract.AuditTarget{}
	for rows.Next() {
		var target contract.AuditTarget
		if err := rows.Scan(&target.Type, &target.ID); err != nil {
			_ = rows.Close()
			return nil, "", err
		}
		targets = append(targets, target)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, "", err
	}
	if closeErr != nil {
		return nil, "", closeErr
	}
	if len(targets) > contract.AuditRetention {
		return nil, "", ErrInvalidState
	}
	names := map[contract.AuditTarget]string{}
	for _, owner := range repository.targetNames {
		batch, err := owner.AuditTargetNamesTx(ctx, tx, targets)
		if err != nil {
			return nil, "", err
		}
		for target, name := range batch {
			names[target] = name
		}
	}
	matched := map[contract.AuditTarget]bool{}
	keys := []string{}
	for _, target := range targets {
		if strings.Contains(target.ID, filters.Target) || names[target] != "" && auditNameMatches(names[target], filters.Target) {
			matched[target] = true
			keys = append(keys, target.Type+"/"+target.ID)
		}
	}
	return matched, searchTargetDigest(keys), nil
}

func searchTargetDigest(keys []string) string {
	// Reuse the canonical filter digest after sorting a deterministic identity list.
	slices.Sort(keys)
	encoded, _ := json.Marshal(keys)
	return filterHash(contract.AuditFilters{Target: string(encoded)})
}

func normalizeAuditName(value string) string {
	return cases.Lower(language.Und).String(strings.Map(func(r rune) rune {
		if unicode.IsMark(r) {
			return -1
		}
		return r
	}, norm.NFKD.String(value)))
}

func auditNameMatches(name, query string) bool {
	candidate := normalizeAuditName(name)
	tokens := strings.Fields(normalizeAuditName(query))
	if len(tokens) == 0 {
		return false
	}
	for _, token := range tokens {
		if strings.Contains(candidate, token) {
			continue
		}
		units := utf16.Encode([]rune(token))
		if len(units) < 4 || strings.ContainsAny(token, "0123456789") {
			return false
		}
		matched := false
		for _, word := range strings.FieldsFunc(candidate, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
			if auditWithinOneEdit(utf16.Encode([]rune(word)), units) {
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

func auditWithinOneEdit(left, right []uint16) bool {
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
