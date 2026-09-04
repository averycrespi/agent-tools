package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluateEnforcesDenyAllowBlockAndSmallestEvidence(t *testing.T) {
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	allowOne := mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51)})
	allowTwo := mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), UpstreamName: stringPointer("tool")})
	denyOne := mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantDeny, ServerID: id(51)})
	denyTwo := mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantDeny, ServerID: id(51), UpstreamName: stringPointer("tool")})

	result, err := repository.Evaluate(context.Background(), EvaluationRequest{PrincipalID: principal.ID, ServerID: id(51), UpstreamName: "tool", Arguments: json.RawMessage(`{}`)})
	require.NoError(t, err)
	denyIDs := []string{denyOne.ID, denyTwo.ID}
	sort.Strings(denyIDs)
	assert.Equal(t, contract.DecisionDeny, result.Decision)
	require.NotNil(t, result.GrantID)
	assert.Equal(t, denyIDs[0], *result.GrantID)
	assert.Equal(t, "5", result.AuthorizationRevision)
	assert.Equal(t, timestamp(testNow), result.EvaluatedAt)

	require.NoError(t, repository.DeleteGrant(context.Background(), denyOne.ID))
	require.NoError(t, repository.DeleteGrant(context.Background(), denyTwo.ID))
	result, err = repository.Evaluate(context.Background(), EvaluationRequest{PrincipalID: principal.ID, ServerID: id(51), UpstreamName: "tool", Arguments: json.RawMessage(`{}`)})
	require.NoError(t, err)
	allowIDs := []string{allowOne.ID, allowTwo.ID}
	sort.Strings(allowIDs)
	assert.Equal(t, contract.DecisionAllow, result.Decision)
	require.NotNil(t, result.GrantID)
	assert.Equal(t, allowIDs[0], *result.GrantID)
	assert.Equal(t, "7", result.AuthorizationRevision)

	require.NoError(t, repository.DeleteGrant(context.Background(), allowOne.ID))
	require.NoError(t, repository.DeleteGrant(context.Background(), allowTwo.ID))
	result, err = repository.Evaluate(context.Background(), EvaluationRequest{PrincipalID: principal.ID, ServerID: id(51), UpstreamName: "tool", Arguments: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.Equal(t, contract.DecisionBlock, result.Decision)
	assert.Nil(t, result.GrantID)
	assert.Equal(t, "9", result.AuthorizationRevision)
}

func TestEvaluateAppliesServerExactAndExpiryScopeAtOneTimestamp(t *testing.T) {
	clock := &fixedClock{now: testNow}
	repository, _ := newRepository(t, nil)
	repository.clock = clock
	principal := mustCreatePrincipal(t, repository)
	expiresAt := testNow.Add(time.Second)
	serverWide := mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), ExpiresAt: &expiresAt})
	mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantDeny, ServerID: id(51), UpstreamName: stringPointer("other")})
	mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantDeny, ServerID: id(52), UpstreamName: stringPointer("tool")})

	result, err := repository.Evaluate(context.Background(), EvaluationRequest{PrincipalID: principal.ID, ServerID: id(51), UpstreamName: "tool", Arguments: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.Equal(t, contract.DecisionAllow, result.Decision)
	assert.Equal(t, serverWide.ID, *result.GrantID)

	clock.now = expiresAt.Add(-time.Nanosecond)
	result, err = repository.Evaluate(context.Background(), EvaluationRequest{PrincipalID: principal.ID, ServerID: id(51), UpstreamName: "tool", Arguments: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.Equal(t, contract.DecisionAllow, result.Decision)

	clock.now = expiresAt
	result, err = repository.Evaluate(context.Background(), EvaluationRequest{PrincipalID: principal.ID, ServerID: id(51), UpstreamName: "tool", Arguments: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.Equal(t, contract.DecisionBlock, result.Decision)
	assert.Nil(t, result.GrantID)
}

func TestEvaluateConstraintUsesObjectOnlyLexicalScalarEquality(t *testing.T) {
	tests := []struct {
		name       string
		constraint string
		arguments  string
		decision   contract.AuthorizationDecision
	}{
		{name: "all scalar types", constraint: `{"equals":{"/s":"x","/b":true,"/n":null,"/i":1.0}}`, arguments: `{"s":"x","b":true,"n":null,"i":1.0}`, decision: contract.DecisionAllow},
		{name: "lexically distinct number", constraint: `{"equals":{"/i":1.0}}`, arguments: `{"i":1}`, decision: contract.DecisionBlock},
		{name: "string is not number", constraint: `{"equals":{"/i":1}}`, arguments: `{"i":"1"}`, decision: contract.DecisionBlock},
		{name: "missing path", constraint: `{"equals":{"/missing":null}}`, arguments: `{}`, decision: contract.DecisionBlock},
		{name: "container leaf", constraint: `{"equals":{"/x":null}}`, arguments: `{"x":{}}`, decision: contract.DecisionBlock},
		{name: "array is not traversed", constraint: `{"equals":{"/x/0":"value"}}`, arguments: `{"x":["value"]}`, decision: contract.DecisionBlock},
		{name: "numeric object member", constraint: `{"equals":{"/x/0":"value"}}`, arguments: `{"x":{"0":"value"}}`, decision: contract.DecisionAllow},
		{name: "escaped and empty members", constraint: `{"equals":{"/a~1b/~0//":"value"}}`, arguments: `{"a/b":{"~":{"":{"":"value"}}}}`, decision: contract.DecisionAllow},
		{name: "prefix pair has no overlap analysis", constraint: `{"equals":{"/x":1,"/x/y":2}}`, arguments: `{"x":1}`, decision: contract.DecisionBlock},
	}
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstreamName := fmt.Sprintf("tool-%d", index)
			constraint := json.RawMessage(test.constraint)
			mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), UpstreamName: &upstreamName, Constraint: &constraint})
			result, err := repository.Evaluate(context.Background(), EvaluationRequest{PrincipalID: principal.ID, ServerID: id(51), UpstreamName: upstreamName, Arguments: json.RawMessage(test.arguments)})
			require.NoError(t, err)
			assert.Equal(t, test.decision, result.Decision)
		})
	}
}

func TestEvaluateV2RegexUsesFullStringStringOnlyConjunction(t *testing.T) {
	tests := []struct {
		name      string
		arguments string
		decision  contract.AuthorizationDecision
	}{
		{name: "full match", arguments: `{"region":"us","resource":"item/42"}`, decision: contract.DecisionAllow},
		{name: "substring does not match", arguments: `{"region":"us","resource":"prefix-item/42"}`, decision: contract.DecisionBlock},
		{name: "non-string does not match", arguments: `{"region":"us","resource":42}`, decision: contract.DecisionBlock},
		{name: "missing does not match", arguments: `{"region":"us"}`, decision: contract.DecisionBlock},
		{name: "equality is conjoined", arguments: `{"region":"eu","resource":"item/42"}`, decision: contract.DecisionBlock},
	}
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstreamName := fmt.Sprintf("regex-%d", index)
			constraint := json.RawMessage(`{"version":2,"equals":{"/region":"us"},"regex":{"/resource":"item/[0-9]+"}}`)
			mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), UpstreamName: &upstreamName, Constraint: &constraint})
			result, err := repository.Evaluate(context.Background(), EvaluationRequest{PrincipalID: principal.ID, ServerID: id(51), UpstreamName: upstreamName, Arguments: json.RawMessage(test.arguments)})
			require.NoError(t, err)
			assert.Equal(t, test.decision, result.Decision)
		})
	}
}

func TestConstraintMatchesFailsClosedWhenRegexWorkBudgetIsExhausted(t *testing.T) {
	constraint, err := CompileConstraint([]byte(`{"version":2,"regex":{"/first":".*","/second":".*"}}`))
	require.NoError(t, err)
	value := strings.Repeat("x", int(mustLimit("constraint_regex_work_bytes")/2)+1)
	arguments, err := strictjson.ParseValue([]byte(`{"first":`+fmt.Sprintf("%q", value)+`,"second":`+fmt.Sprintf("%q", value)+`}`), strictjson.Options{MaxBytes: mustLimit("mcp_body_bytes"), MaxDepth: int(mustLimit("json_depth"))})
	require.NoError(t, err)
	remainingRegexWork := mustLimit("constraint_regex_work_bytes")
	matched, err := constraintMatches(constraint, arguments, &remainingRegexWork)
	assert.ErrorIs(t, err, ErrAuthorizationUnavailable)
	assert.False(t, matched)
}

func TestEvaluateFailsClosedWhenCumulativeRegexWorkBudgetIsExhausted(t *testing.T) {
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	value := strings.Repeat("x", int(mustLimit("constraint_regex_work_bytes")/2)+1)
	firstConstraint := json.RawMessage(`{"version":2,"regex":{"/value":"y+"}}`)
	secondConstraint := json.RawMessage(`{"version":2,"regex":{"/value":".*"}}`)
	mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), UpstreamName: stringPointer("tool"), Constraint: &firstConstraint})
	mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantDeny, ServerID: id(51), UpstreamName: stringPointer("tool"), Constraint: &secondConstraint})
	mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51)})

	_, err := repository.Evaluate(context.Background(), EvaluationRequest{PrincipalID: principal.ID, ServerID: id(51), UpstreamName: "tool", Arguments: json.RawMessage(`{"value":` + fmt.Sprintf("%q", value) + `}`)})
	assert.ErrorIs(t, err, ErrAuthorizationUnavailable)
}

func TestEvaluateRejectsMalformedInputAndInvalidLoadedPolicyWithoutPartialAllow(t *testing.T) {
	t.Run("malformed arguments", func(t *testing.T) {
		repository, _ := newRepository(t, nil)
		principal := mustCreatePrincipal(t, repository)
		mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51)})
		for _, arguments := range []string{``, `[]`, `{"x":1,"x":2}`, `{"x":`} {
			_, err := repository.Evaluate(context.Background(), EvaluationRequest{PrincipalID: principal.ID, ServerID: id(51), UpstreamName: "tool", Arguments: json.RawMessage(arguments)})
			assert.ErrorIs(t, err, ErrAuthorizationUnavailable)
		}
	})

	for _, effect := range []contract.GrantEffect{contract.GrantAllow, contract.GrantDeny} {
		t.Run("corrupt "+string(effect), func(t *testing.T) {
			repository, store := newRepository(t, nil)
			principal := mustCreatePrincipal(t, repository)
			mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51)})
			corrupt := mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: effect, ServerID: id(51), UpstreamName: stringPointer("tool")})
			require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
				_, err := transaction.Exec(`UPDATE grants SET constraint_json = '{"equals":{}}' WHERE id = ?`, corrupt.ID)
				return err
			}))
			_, err := repository.Evaluate(context.Background(), EvaluationRequest{PrincipalID: principal.ID, ServerID: id(51), UpstreamName: "tool", Arguments: json.RawMessage(`{}`)})
			assert.ErrorIs(t, err, ErrAuthorizationUnavailable)
		})
	}
}

func TestEvaluateGeneratedScalarCorpus(t *testing.T) {
	scalars := []string{`null`, `true`, `false`, `""`, `"text"`, `0`, `-0`, `1.0`, `1e+2`, `1E-2`}
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	for index, scalar := range scalars {
		t.Run(scalar, func(t *testing.T) {
			upstreamName := fmt.Sprintf("scalar-%d", index)
			constraint := json.RawMessage(`{"equals":{"/value":` + scalar + `}}`)
			mustCreateEvaluationGrant(t, repository, CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), UpstreamName: &upstreamName, Constraint: &constraint})
			result, err := repository.Evaluate(context.Background(), EvaluationRequest{PrincipalID: principal.ID, ServerID: id(51), UpstreamName: upstreamName, Arguments: json.RawMessage(`{"value":` + scalar + `}`)})
			require.NoError(t, err)
			assert.Equal(t, contract.DecisionAllow, result.Decision)
		})
	}
}

func FuzzConstraintMatchesObjectOnly(f *testing.F) {
	for _, seed := range []struct{ constraint, arguments string }{
		{`{"equals":{"/x":1}}`, `{"x":1}`},
		{`{"equals":{"/x/0":"v"}}`, `{"x":["v"]}`},
		{`{"equals":{"/a~1b":null}}`, `{"a/b":null}`},
	} {
		f.Add(seed.constraint, seed.arguments)
	}
	f.Fuzz(func(t *testing.T, constraintJSON, argumentsJSON string) {
		constraint, err := CompileConstraint([]byte(constraintJSON))
		if err != nil {
			return
		}
		arguments, err := strictjson.ParseValue([]byte(argumentsJSON), strictjson.Options{MaxBytes: mustLimit("mcp_body_bytes"), MaxDepth: int(mustLimit("json_depth"))})
		if err != nil || arguments.Type != strictjson.ValueObject {
			return
		}
		remainingRegexWork := mustLimit("constraint_regex_work_bytes")
		_, _ = constraintMatches(constraint, arguments, &remainingRegexWork)
	})
}

func mustCreateEvaluationGrant(t *testing.T, repository *Repository, request CreateGrantRequest) contract.Grant {
	t.Helper()
	grant, err := repository.CreateGrant(context.Background(), request, allowCurrentTarget)
	require.NoError(t, err)
	return grant
}
