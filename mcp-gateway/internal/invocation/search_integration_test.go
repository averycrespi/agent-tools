//go:build integration

package invocation

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type readNames struct {
	names map[string]string
	err   error
}

func (source *readNames) PrincipalDisplayNamesTx(context.Context, *sql.Tx) (map[string]string, error) {
	return source.names, source.err
}

func TestInvocationAuthoritativeSearchIntegration(t *testing.T) {
	repository, _, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(64))
	names := &readNames{names: map[string]string{invocationID(1): "Café Investigator"}}
	service, err := NewReadService(repository, names)
	require.NoError(t, err)
	admission := testEvaluatedAdmission()
	admission.PrincipalID = invocationID(1)
	admission.RequestedName = pointer("retired_library.lookup")
	old := insertReadFixture(t, repository, admission, pointer(contract.TerminalSucceeded))
	newer := insertReadFixture(t, repository, admission, pointer(contract.TerminalSucceeded))
	for range 50 {
		nonmatch := testEvaluatedAdmission()
		nonmatch.PrincipalID = invocationID(3)
		nonmatch.RequestedName = pointer("workshop.echo")
		insertReadFixture(t, repository, nonmatch, pointer(contract.TerminalSucceeded))
	}
	ctx := context.Background()
	unfiltered, err := service.List(ctx, contract.InvocationListQuery{Limit: 50})
	require.NoError(t, err)
	assert.NotContains(t, summaryIDs(unfiltered.Items), old.InvocationID)
	query := contract.InvocationListQuery{Limit: 1, Filters: contract.InvocationFilters{
		Tool: "retired lokoup", Principal: "cafe investgiator", SearchLocale: "en-US", Outcome: pointer(contract.InvocationOutcomeSucceeded), Decision: pointer(contract.DecisionAllow),
	}}
	first, err := service.List(ctx, query)
	require.NoError(t, err)
	assert.Equal(t, []string{newer.InvocationID}, summaryIDs(first.Items))
	require.NotNil(t, first.NextCursor)
	query.Cursor = first.NextCursor
	second, err := service.List(ctx, query)
	require.NoError(t, err)
	assert.Equal(t, []string{old.InvocationID}, summaryIDs(second.Items))
	assert.Nil(t, second.NextCursor)
	query.Filters.Tool = "echo"
	_, err = service.List(ctx, query)
	assert.ErrorIs(t, err, ErrStaleCursor)
	query.Filters.Tool = "retired lokoup"
	names.names[invocationID(1)] = "Renamed investigator"
	_, err = service.List(ctx, query)
	assert.ErrorIs(t, err, ErrStaleCursor)
	query.Cursor = nil
	page, err := service.List(ctx, query)
	require.NoError(t, err)
	assert.Empty(t, page.Items, "old names are not historical evidence")
	query.Filters.Principal = invocationID(1)
	delete(names.names, invocationID(1))
	page, err = service.List(ctx, query)
	require.NoError(t, err)
	require.Len(t, page.Items, 1, "recorded identity remains searchable without current name")
	names.err = errors.New("name read unavailable")
	_, err = service.List(ctx, query)
	assert.ErrorIs(t, err, ErrStorageUnavailable, "failed name source must not become no matches")
}

func TestInvocationNotEvaluatedSearchIntegration(t *testing.T) {
	repository, _, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(2))
	invalid := insertReadFixture(t, repository, Admission{PrincipalID: invocationID(1), CredentialID: invocationID(2), CredentialFingerprint: "0123456789abcdef", CredentialRevision: "1", Class: contract.AdmissionInvalidParams}, nil)
	insertReadFixture(t, repository, testEvaluatedAdmission(), nil)
	page, err := repository.List(context.Background(), contract.InvocationListQuery{Limit: 50, Filters: contract.InvocationFilters{Decision: pointer(contract.AuthorizationDecision("not_evaluated")), Tool: "not resolved"}})
	require.NoError(t, err)
	assert.Equal(t, []string{invalid.InvocationID}, summaryIDs(page.Items))
}

func TestInvocationFullCapacitySearchIntegration(t *testing.T) {
	repository, store, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(1))
	old := insertReadFixture(t, repository, testEvaluatedAdmission(), nil)
	require.NoError(t, store.Mutate(context.Background(), func(tx *sql.Tx) error {
		return insertInvocationCapacityFixtures(context.Background(), tx, invocationLimit()-1)
	}))
	for _, query := range []string{"namespace.tool", "definitely absent"} {
		started := time.Now()
		page, err := repository.List(context.Background(), contract.InvocationListQuery{Limit: 50, Filters: contract.InvocationFilters{Tool: query}})
		require.NoError(t, err)
		t.Logf("65536 retained rows, query %q: %s", query, time.Since(started))
		if query == "namespace.tool" {
			assert.Equal(t, []string{old.InvocationID}, summaryIDs(page.Items))
		} else {
			assert.Empty(t, page.Items)
		}
		assert.Nil(t, page.NextCursor)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := repository.List(ctx, contract.InvocationListQuery{Limit: 50, Filters: contract.InvocationFilters{Tool: "absent"}})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestInvocationEveryFilterChoiceIntegration(t *testing.T) {
	repository, _, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(16))
	expected := map[contract.InvocationOutcomeClass][]string{}
	for _, outcome := range contract.InvocationOutcomeClasses() {
		admission := testEvaluatedAdmission()
		var terminal *contract.InvocationTerminalClass
		switch outcome {
		case contract.InvocationOutcomeInvalidParams, contract.InvocationOutcomeUnknownTool, contract.InvocationOutcomeInvalidArguments, contract.InvocationOutcomeAuthorizationUnavailable:
			admission.Class = contract.InvocationAdmissionClass(outcome)
			admission.Authorization = nil
			if outcome == contract.InvocationOutcomeInvalidParams || outcome == contract.InvocationOutcomeUnknownTool {
				admission.Route = nil
			}
		case contract.InvocationOutcomeDeny, contract.InvocationOutcomeBlock:
			admission.Authorization.Decision = contract.AuthorizationDecision(outcome)
			if outcome == contract.InvocationOutcomeBlock {
				admission.Authorization.GrantID = nil
			}
		default:
			terminal = pointer(contract.InvocationTerminalClass(outcome))
		}
		row := insertReadFixture(t, repository, admission, terminal)
		expected[outcome] = []string{row.InvocationID}
	}
	missing := insertReadFixture(t, repository, testEvaluatedAdmission(), nil)
	expected[contract.InvocationOutcomeUnknown] = append([]string{missing.InvocationID}, expected[contract.InvocationOutcomeUnknown]...)
	for _, outcome := range contract.InvocationOutcomeClasses() {
		page, err := repository.List(context.Background(), contract.InvocationListQuery{Limit: 50, Filters: contract.InvocationFilters{Tool: "namespace", Outcome: pointer(outcome)}})
		require.NoError(t, err)
		assert.Equal(t, expected[outcome], summaryIDs(page.Items), outcome)
	}
	for decision, count := range map[string]int{"not_evaluated": 4, "allow": 5, "deny": 1, "block": 1} {
		page, err := repository.List(context.Background(), contract.InvocationListQuery{Limit: 50, Filters: contract.InvocationFilters{Tool: "namespace", Decision: pointer(contract.AuthorizationDecision(decision))}})
		require.NoError(t, err)
		assert.Len(t, page.Items, count, decision)
	}
}

func TestInvocationCursorAuthenticationIntegration(t *testing.T) {
	repository, store, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(4))
	for range 2 {
		insertReadFixture(t, repository, testEvaluatedAdmission(), nil)
	}
	page, err := repository.List(context.Background(), contract.InvocationListQuery{Limit: 1})
	require.NoError(t, err)
	require.NotNil(t, page.NextCursor)
	contents, err := base64.RawURLEncoding.DecodeString(*page.NextCursor)
	require.NoError(t, err)
	var cursor invocationCursor
	require.NoError(t, json.Unmarshal(contents, &cursor))
	cursor.NextSequence++
	contents, err = json.Marshal(cursor)
	require.NoError(t, err)
	tampered := base64.RawURLEncoding.EncodeToString(contents)
	_, err = repository.List(context.Background(), contract.InvocationListQuery{Limit: 1, Cursor: &tampered})
	assert.ErrorIs(t, err, ErrInvalidCursor)
	restarted, err := NewRepository(store, repository.clock, uniqueInvocationEntropy(1))
	require.NoError(t, err)
	_, err = restarted.List(context.Background(), contract.InvocationListQuery{Limit: 1, Cursor: page.NextCursor})
	assert.ErrorIs(t, err, ErrStaleCursor)
}

func TestInvocationSearchBoundsIntegration(t *testing.T) {
	repository, _, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(2))
	for _, filters := range []contract.InvocationFilters{{Tool: strings.Repeat("x", 257)}, {Principal: "x\n"}, {SearchLocale: "not_a_locale"}} {
		_, err := repository.List(context.Background(), contract.InvocationListQuery{Limit: 50, Filters: filters})
		assert.ErrorIs(t, err, ErrInvalidInput)
	}
	filters := contract.InvocationFilters{Tool: strings.Repeat("x", 256), Principal: strings.Repeat("y", 256), SearchLocale: "en-US"}
	cursor, err := repository.encodeInvocationCursor(contract.InvocationCursorBinding{Filters: filters, UpperSequence: 65536, NextSequence: 1})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(cursor), 512)
}
