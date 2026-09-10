package invocation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

const invocationSummarySelect = `SELECT insertion_sequence, id, principal_id, credential_id, credential_fingerprint,
	credential_revision, admitted_at, admission_class, requested_name,
	CASE WHEN redacted_arguments IS NULL THEN NULL ELSE '{}' END,
	server_id, tool_id, upstream_name, descriptor_revision, descriptor_fingerprint,
	decision, authorization_revision, evaluated_at, grant_id, completed_at, terminal_class
	FROM invocations`

func (repository *Repository) Get(ctx context.Context, invocationID string) (contract.Invocation, error) {
	if !validOpaqueInvocationID(invocationID) {
		return contract.Invocation{}, ErrInvalidInput
	}
	var item contract.Invocation
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		record, scanErr := scanInvocation(transaction.QueryRowContext(ctx, invocationSelect+` WHERE id = ?`, invocationID))
		if errors.Is(scanErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if scanErr != nil {
			return fmt.Errorf("read invocation item: %w", scanErr)
		}
		if !validStoredInvocation(record) {
			return invalidInvocationState("invocation row is malformed")
		}
		projected, projectErr := contract.ProjectInvocationAudit(record)
		if projectErr != nil {
			return invalidInvocationState("invocation projection is malformed")
		}
		item = projected
		return nil
	})
	return item, err
}

func (repository *Repository) List(ctx context.Context, query contract.InvocationListQuery) (contract.InvocationPage, error) {
	return repository.list(ctx, query, nil)
}

func (repository *Repository) list(ctx context.Context, query contract.InvocationListQuery, nameSource PrincipalDisplayNames) (contract.InvocationPage, error) {
	if query.Limit < 1 || query.Limit > 100 || !validInvocationFilters(query.Filters) {
		return contract.InvocationPage{}, ErrInvalidInput
	}
	var binding contract.InvocationCursorBinding
	if query.Cursor != nil {
		decoded, err := repository.decodeInvocationCursor(*query.Cursor)
		if err != nil {
			return contract.InvocationPage{}, err
		}
		if decoded.QueryDigest != searchDigest(query.Filters) {
			return contract.InvocationPage{}, ErrStaleCursor
		}
		binding = decoded
	}

	page := contract.InvocationPage{Items: make([]contract.InvocationSummary, 0, query.Limit)}
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		names := map[string]string{}
		if query.Filters.Principal != "" {
			if nameSource == nil {
				return ErrStorageUnavailable
			}
			var err error
			names, err = nameSource.PrincipalDisplayNamesTx(ctx, transaction)
			if err != nil {
				return fmt.Errorf("read invocation principal names: %w", err)
			}
		}
		namesDigest := searchDigest(names)
		if query.Cursor != nil && binding.NamesDigest != namesDigest {
			return ErrStaleCursor
		}
		toolSearch := newHistorySearch(query.Filters.Tool, query.Filters.SearchLocale)
		principalSearch := newHistorySearch(query.Filters.Principal, query.Filters.SearchLocale)
		principalQuery := strings.TrimSpace(query.Filters.Principal)
		principalMatches := make(map[string]bool, len(names))
		for id, name := range names {
			principalMatches[id] = principalSearch.matches(name)
		}
		if query.Cursor == nil {
			if err := transaction.QueryRowContext(ctx, `SELECT COALESCE(MAX(insertion_sequence), 0) FROM invocations`).Scan(&binding.UpperSequence); err != nil {
				return fmt.Errorf("capture invocation upper sequence: %w", err)
			}
			if binding.UpperSequence == 0 {
				return nil
			}
			binding.Filters = query.Filters
			binding.NextSequence = binding.UpperSequence
		} else {
			var floor int64
			if err := transaction.QueryRowContext(ctx, `SELECT COALESCE(MIN(insertion_sequence), 0) FROM invocations`).Scan(&floor); err != nil {
				return fmt.Errorf("read invocation retention floor: %w", err)
			}
			if floor == 0 || floor > binding.NextSequence {
				return ErrStaleCursor
			}
		}

		selectionLimit := query.Limit + 1
		searching := query.Filters.Tool != "" || query.Filters.Principal != ""
		if searching {
			selectionLimit = int(invocationLimit()) + 1
		}
		statement, arguments := invocationListStatement(binding.NextSequence, query.Filters, selectionLimit)
		if searching {
			statement = strings.Replace(statement, invocationSummarySelect, invocationSearchSelect, 1)
		}
		rows, err := transaction.QueryContext(ctx, statement, arguments...)
		if err != nil {
			return fmt.Errorf("list invocations: %w", err)
		}
		defer func() { _ = rows.Close() }()
		records := make([]contract.InvocationAuditRecord, 0, query.Limit+1)
		scanned := int64(0)
		for rows.Next() {
			scanned++
			if scanned > invocationLimit() {
				return invalidInvocationState("invocation history exceeds capacity")
			}
			var record contract.InvocationAuditRecord
			var scanErr error
			if searching {
				record, scanErr = scanInvocationSearch(rows)
			} else {
				record, scanErr = scanInvocation(rows)
			}
			if scanErr != nil {
				return fmt.Errorf("scan invocation list row: %w", scanErr)
			}
			if !searching && !validStoredInvocation(record) {
				return invalidInvocationState("invocation row is malformed")
			}
			if !toolSearch.matches(invocationSearchLabel(record)) ||
				(principalQuery != "" && !strings.Contains(record.PrincipalID, principalQuery) && len(principalSearch.tokens) != 0 && !principalMatches[record.PrincipalID]) {
				continue
			}
			records = append(records, record)
			if len(records) == query.Limit+1 {
				break
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate invocation list: %w", err)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if searching {
			var err error
			records, err = hydrateInvocationSelection(ctx, transaction, records)
			if err != nil {
				return err
			}
		}
		visible := len(records)
		if visible > query.Limit {
			visible = query.Limit
			next := records[query.Limit].Sequence
			cursor, encodeErr := repository.encodeInvocationCursor(contract.InvocationCursorBinding{
				Filters: query.Filters, NamesDigest: namesDigest, UpperSequence: binding.UpperSequence, NextSequence: next,
			})
			if encodeErr != nil {
				return invalidInvocationState("invocation cursor cannot be encoded")
			}
			page.NextCursor = &cursor
		}
		for _, record := range records[:visible] {
			summary, projectErr := contract.ProjectInvocationSummary(record)
			if projectErr != nil {
				return invalidInvocationState("invocation summary projection is malformed")
			}
			page.Items = append(page.Items, summary)
		}
		return nil
	})
	if err != nil {
		return contract.InvocationPage{}, err
	}
	return page, nil
}

func invocationListStatement(nextSequence int64, filters contract.InvocationFilters, limit int) (string, []any) {
	clauses := []string{"insertion_sequence <= ?"}
	arguments := []any{nextSequence}
	if filters.PrincipalID != nil {
		clauses = append(clauses, "principal_id = ?")
		arguments = append(arguments, *filters.PrincipalID)
	}
	if filters.ServerID != nil {
		clauses = append(clauses, "server_id = ?")
		arguments = append(arguments, *filters.ServerID)
	}
	if filters.RequestedName != nil {
		clauses = append(clauses, "requested_name = ?")
		arguments = append(arguments, *filters.RequestedName)
	}
	if filters.AdmissionClass != nil {
		clauses = append(clauses, "admission_class = ?")
		arguments = append(arguments, string(*filters.AdmissionClass))
	}
	if filters.Decision != nil {
		if *filters.Decision == "not_evaluated" {
			clauses = append(clauses, "decision IS NULL")
		} else {
			clauses = append(clauses, "decision = ?")
			arguments = append(arguments, string(*filters.Decision))
		}
	}
	if filters.Outcome != nil {
		clause, values := invocationOutcomeClause(*filters.Outcome)
		clauses = append(clauses, clause)
		arguments = append(arguments, values...)
	}
	arguments = append(arguments, limit)
	return invocationSummarySelect + " WHERE " + strings.Join(clauses, " AND ") + " ORDER BY insertion_sequence DESC LIMIT ?", arguments
}

func invocationOutcomeClause(outcome contract.InvocationOutcomeClass) (string, []any) {
	switch outcome {
	case contract.InvocationOutcomeInvalidParams, contract.InvocationOutcomeUnknownTool,
		contract.InvocationOutcomeInvalidArguments, contract.InvocationOutcomeAuthorizationUnavailable:
		return "admission_class = ?", []any{string(outcome)}
	case contract.InvocationOutcomeDeny, contract.InvocationOutcomeBlock:
		return "admission_class = 'evaluated' AND decision = ?", []any{string(outcome)}
	case contract.InvocationOutcomePrestartFailure, contract.InvocationOutcomeSucceeded, contract.InvocationOutcomeDownstreamFailure:
		return "admission_class = 'evaluated' AND decision = 'allow' AND terminal_class = ?", []any{string(outcome)}
	case contract.InvocationOutcomeUnknown:
		return "admission_class = 'evaluated' AND decision = 'allow' AND (terminal_class = 'outcome_unknown' OR terminal_class IS NULL)", nil
	default:
		return "0 = 1", nil
	}
}

func validInvocationFilters(filters contract.InvocationFilters) bool {
	if !validSearchText(filters.Tool) || !validSearchText(filters.Principal) || !validSearchLocale(filters.SearchLocale) {
		return false
	}
	if filters.PrincipalID != nil && !validOpaqueInvocationID(*filters.PrincipalID) ||
		filters.ServerID != nil && !validOpaqueInvocationID(*filters.ServerID) ||
		filters.RequestedName != nil && !validInvocationName(*filters.RequestedName) {
		return false
	}
	if filters.AdmissionClass != nil {
		if _, err := contract.ParseInvocationAdmissionClass(string(*filters.AdmissionClass)); err != nil {
			return false
		}
	}
	if filters.Decision != nil {
		if _, err := contract.ParseInvocationDecisionFilter(string(*filters.Decision)); err != nil {
			return false
		}
	}
	if filters.Outcome != nil {
		if _, err := contract.ParseInvocationOutcomeClass(string(*filters.Outcome)); err != nil {
			return false
		}
	}
	return true
}

func invocationCursorLimit() int64 { return requiredLimit("cursor_bytes") }
