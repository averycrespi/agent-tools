//go:build integration

package invocation

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvocationRepositoryReadsIntegration(t *testing.T) {
	ctx := context.Background()

	t.Run("newest pages filters and item capture", func(t *testing.T) {
		repository, store, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(8))
		invalid := insertReadFixture(t, repository, Admission{
			PrincipalID: invocationID(1), CredentialID: invocationID(2), CredentialFingerprint: "0123456789abcdef",
			CredentialRevision: "1", Class: contract.AdmissionInvalidParams,
		}, nil)
		allowedAdmission := testEvaluatedAdmission()
		allowedName := "namespace.allowed"
		allowedAdmission.RequestedName = &allowedName
		allowed := insertReadFixture(t, repository, allowedAdmission, pointer(contract.TerminalSucceeded))
		deniedAdmission := testEvaluatedAdmission()
		deniedName := "namespace.denied"
		deniedAdmission.RequestedName = &deniedName
		deniedAdmission.PrincipalID = invocationID(3)
		deniedAdmission.Route.ServerID = invocationID(12)
		deniedAdmission.Authorization.Decision = contract.DecisionDeny
		deniedAdmission.Authorization.GrantID = pointer(invocationID(72))
		denied := insertReadFixture(t, repository, deniedAdmission, nil)

		first, err := repository.List(ctx, contract.InvocationListQuery{Limit: 2})
		require.NoError(t, err)
		require.Len(t, first.Items, 2)
		assert.Equal(t, []string{denied.InvocationID, allowed.InvocationID}, summaryIDs(first.Items))
		require.NotNil(t, first.NextCursor)
		encoded, err := json.Marshal(first)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), "redacted_arguments")

		newer := insertReadFixture(t, repository, testEvaluatedAdmission(), nil)
		second, err := repository.List(ctx, contract.InvocationListQuery{Limit: 2, Cursor: first.NextCursor})
		require.NoError(t, err)
		assert.Equal(t, []string{invalid.InvocationID}, summaryIDs(second.Items))
		assert.Nil(t, second.NextCursor)
		assert.NotContains(t, summaryIDs(second.Items), newer.InvocationID)

		exact, err := repository.List(ctx, contract.InvocationListQuery{Limit: 3})
		require.NoError(t, err)
		assert.Len(t, exact.Items, 3)
		require.NotNil(t, exact.NextCursor, "a fourth current row requires continuation")

		for name, test := range map[string]struct {
			filters contract.InvocationFilters
			ids     []string
		}{
			"principal": {filters: contract.InvocationFilters{PrincipalID: pointer(deniedAdmission.PrincipalID)}, ids: []string{denied.InvocationID}},
			"server":    {filters: contract.InvocationFilters{ServerID: pointer(deniedAdmission.Route.ServerID)}, ids: []string{denied.InvocationID}},
			"name":      {filters: contract.InvocationFilters{RequestedName: &allowedName}, ids: []string{allowed.InvocationID}},
			"admission": {filters: contract.InvocationFilters{AdmissionClass: pointer(contract.AdmissionInvalidParams)}, ids: []string{invalid.InvocationID}},
			"decision":  {filters: contract.InvocationFilters{Decision: pointer(contract.DecisionDeny)}, ids: []string{denied.InvocationID}},
			"outcome":   {filters: contract.InvocationFilters{Outcome: pointer(contract.InvocationOutcomeSucceeded)}, ids: []string{allowed.InvocationID}},
		} {
			t.Run(name, func(t *testing.T) {
				page, listErr := repository.List(ctx, contract.InvocationListQuery{Limit: 100, Filters: test.filters})
				require.NoError(t, listErr)
				assert.Equal(t, test.ids, summaryIDs(page.Items))
			})
		}

		item, err := repository.Get(ctx, allowed.InvocationID)
		require.NoError(t, err)
		assert.JSONEq(t, `{"value":1e0}`, string(item.RedactedArguments))
		_, err = repository.Get(ctx, invocationID(99))
		assert.ErrorIs(t, err, ErrNotFound)
		_, err = repository.Get(ctx, "bad")
		assert.ErrorIs(t, err, ErrInvalidInput)

		require.NoError(t, store.Mutate(ctx, func(transaction *sql.Tx) error {
			if _, dropErr := transaction.ExecContext(ctx, `DROP TRIGGER invocations_terminal_once`); dropErr != nil {
				return dropErr
			}
			_, updateErr := transaction.ExecContext(ctx, `UPDATE invocations SET redacted_arguments = '{ "forbidden": true }' WHERE id = ?`, allowed.InvocationID)
			return updateErr
		}))
		page, err := repository.List(ctx, contract.InvocationListQuery{Limit: 100})
		require.NoError(t, err, "collection reads must not load item captures")
		assert.Len(t, page.Items, 4)
		_, err = repository.Get(ctx, allowed.InvocationID)
		assert.ErrorIs(t, err, ErrInvalidState)
	})

	t.Run("cursor syntax and binding", func(t *testing.T) {
		repository, _, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(4))
		insertReadFixture(t, repository, testEvaluatedAdmission(), nil)
		insertReadFixture(t, repository, testEvaluatedAdmission(), nil)
		page, err := repository.List(ctx, contract.InvocationListQuery{Limit: 1})
		require.NoError(t, err)
		require.NotNil(t, page.NextCursor)

		for _, cursor := range []string{"", "***", base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"unknown":true}`))} {
			_, listErr := repository.List(ctx, contract.InvocationListQuery{Limit: 1, Cursor: &cursor})
			assert.ErrorIs(t, listErr, ErrInvalidCursor, cursor)
		}
		changed := contract.InvocationFilters{PrincipalID: pointer(invocationID(1))}
		_, err = repository.List(ctx, contract.InvocationListQuery{Limit: 1, Cursor: page.NextCursor, Filters: changed})
		assert.ErrorIs(t, err, ErrStaleCursor)
		_, err = repository.List(ctx, contract.InvocationListQuery{Limit: 0})
		assert.ErrorIs(t, err, ErrInvalidInput)

		maximumName := "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
		maximumFilters := contract.InvocationFilters{
			PrincipalID: pointer(invocationID(1)), ServerID: pointer(invocationID(2)), RequestedName: &maximumName,
			AdmissionClass: pointer(contract.AdmissionAuthorizationUnavailable), Decision: pointer(contract.DecisionAllow),
			Outcome: pointer(contract.InvocationOutcomeDownstreamFailure),
		}
		maximumCursor, err := encodeInvocationCursor(contract.InvocationCursorBinding{Filters: maximumFilters, UpperSequence: invocationLimit(), NextSequence: 1})
		require.NoError(t, err)
		assert.LessOrEqual(t, int64(len(maximumCursor)), invocationCursorLimit())
		decoded, err := decodeInvocationCursor(maximumCursor)
		require.NoError(t, err)
		assert.True(t, sameInvocationFilters(maximumFilters, decoded.Filters))
	})

	t.Run("terminal filter entry and exit", func(t *testing.T) {
		repository, _, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(8))
		entering := insertReadFixture(t, repository, testEvaluatedAdmission(), nil)
		middle := insertReadFixture(t, repository, testEvaluatedAdmission(), pointer(contract.TerminalSucceeded))
		newest := insertReadFixture(t, repository, testEvaluatedAdmission(), pointer(contract.TerminalSucceeded))
		succeeded := contract.InvocationFilters{Outcome: pointer(contract.InvocationOutcomeSucceeded)}
		first, err := repository.List(ctx, contract.InvocationListQuery{Limit: 1, Filters: succeeded})
		require.NoError(t, err)
		assert.Equal(t, []string{newest.InvocationID}, summaryIDs(first.Items))
		require.NotNil(t, first.NextCursor)
		require.NoError(t, repository.AnnotateTerminal(ctx, entering.InvocationID, contract.TerminalSucceeded))
		continued, err := repository.List(ctx, contract.InvocationListQuery{Limit: 10, Cursor: first.NextCursor, Filters: succeeded})
		require.NoError(t, err)
		assert.Equal(t, []string{middle.InvocationID, entering.InvocationID}, summaryIDs(continued.Items))

		exiting := insertReadFixture(t, repository, testEvaluatedAdmission(), nil)
		latestMissing := insertReadFixture(t, repository, testEvaluatedAdmission(), nil)
		unknown := contract.InvocationFilters{Outcome: pointer(contract.InvocationOutcomeUnknown)}
		missingPage, err := repository.List(ctx, contract.InvocationListQuery{Limit: 1, Filters: unknown})
		require.NoError(t, err)
		assert.Equal(t, []string{latestMissing.InvocationID}, summaryIDs(missingPage.Items))
		require.NotNil(t, missingPage.NextCursor)
		require.NoError(t, repository.AnnotateTerminal(ctx, exiting.InvocationID, contract.TerminalSucceeded))
		continued, err = repository.List(ctx, contract.InvocationListQuery{Limit: 10, Cursor: missingPage.NextCursor, Filters: unknown})
		require.NoError(t, err)
		assert.Empty(t, continued.Items)
	})

	t.Run("latched reads fail closed", func(t *testing.T) {
		fault := func(point storage.FaultPoint) error {
			if point == storage.FaultAfterCommit {
				return errors.New("uncertain commit")
			}
			return nil
		}
		repository, store, _ := newInvocationRepository(t, fault, uniqueInvocationEntropy(2))
		prepared, err := repository.Prepare(testEvaluatedAdmission())
		require.NoError(t, err)
		assert.ErrorIs(t, repository.Insert(ctx, prepared), ErrStorageUnavailable)
		assert.True(t, store.Latched())
		_, err = repository.List(ctx, contract.InvocationListQuery{Limit: 10})
		assert.ErrorIs(t, err, ErrStorageUnavailable)
		_, err = repository.Get(ctx, prepared.InvocationID)
		assert.ErrorIs(t, err, ErrStorageUnavailable)
	})
}

func insertReadFixture(t *testing.T, repository *Repository, admission Admission, terminal *contract.InvocationTerminalClass) PreparedAdmission {
	t.Helper()
	prepared, err := repository.Prepare(admission)
	require.NoError(t, err)
	require.NoError(t, repository.Insert(context.Background(), prepared))
	if terminal != nil {
		require.NoError(t, repository.AnnotateTerminal(context.Background(), prepared.InvocationID, *terminal))
	}
	return prepared
}

func summaryIDs(items []contract.InvocationSummary) []string {
	ids := make([]string, len(items))
	for index, item := range items {
		ids[index] = item.ID
	}
	return ids
}

func pointer[T any](value T) *T { return &value }
