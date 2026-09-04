package authorization

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

func (repository *Repository) Evaluate(ctx context.Context, request EvaluationRequest) (contract.AuthorizationResult, error) {
	if !validOpaqueID(request.PrincipalID) || !validOpaqueID(request.ServerID) || !validUpstreamName(request.UpstreamName) {
		return contract.AuthorizationResult{}, ErrInvalidInput
	}
	arguments, err := strictjson.ParseValue(request.Arguments, strictjson.Options{
		MaxBytes: mustLimit("mcp_body_bytes"),
		MaxDepth: int(mustLimit("json_depth")),
	})
	if err != nil || arguments.Type != strictjson.ValueObject {
		return contract.AuthorizationResult{}, ErrAuthorizationUnavailable
	}
	evaluatedAt := repository.clock.Now().UTC()
	var result contract.AuthorizationResult
	err = repository.view(ctx, func(transaction *sql.Tx) error {
		var evaluateErr error
		result, evaluateErr = evaluateTx(repository, ctx, transaction, request.PrincipalID, request.ServerID, request.UpstreamName, arguments, evaluatedAt)
		return evaluateErr
	})
	return result, err
}

func evaluateTx(
	repository *Repository,
	ctx context.Context,
	transaction *sql.Tx,
	principalID string,
	serverID string,
	upstreamName string,
	arguments strictjson.Value,
	evaluatedAt time.Time,
) (contract.AuthorizationResult, error) {
	revision, err := authorizationRevisionTx(ctx, transaction)
	if err != nil {
		return contract.AuthorizationResult{}, err
	}
	result := contract.AuthorizationResult{
		Decision: contract.DecisionBlock, AuthorizationRevision: revision,
		EvaluatedAt: formatAuthorizationTime(evaluatedAt),
	}
	rows, err := transaction.QueryContext(ctx, `
		SELECT id, principal_id, effect, server_id, upstream_name,
		       constraint_json, expires_at, created_at
		FROM grants
		WHERE principal_id = ?
		ORDER BY id
		LIMIT ?`, principalID, mustLimit("grants")+1)
	if err != nil {
		return contract.AuthorizationResult{}, fmt.Errorf("%w: read grants for evaluation: %w", ErrStorageUnavailable, err)
	}
	defer func() { _ = rows.Close() }()
	var smallestAllow, smallestDeny string
	remainingRegexWork := mustLimit("constraint_regex_work_bytes")
	count := int64(0)
	for rows.Next() {
		if count >= mustLimit("grants") {
			return contract.AuthorizationResult{}, ErrAuthorizationUnavailable
		}
		count++
		grant, loadErr := loadEvaluationGrant(rows, func(source string) (CompiledConstraint, error) {
			return repository.compileConstraint(revision, source)
		})
		if loadErr != nil {
			return contract.AuthorizationResult{}, ErrAuthorizationUnavailable
		}
		applies, appliesErr := grant.applies(serverID, upstreamName, evaluatedAt, arguments, &remainingRegexWork)
		if appliesErr != nil {
			return contract.AuthorizationResult{}, ErrAuthorizationUnavailable
		}
		if !applies {
			continue
		}
		switch grant.effect {
		case contract.GrantDeny:
			if smallestDeny == "" || grant.id < smallestDeny {
				smallestDeny = grant.id
			}
		case contract.GrantAllow:
			if smallestAllow == "" || grant.id < smallestAllow {
				smallestAllow = grant.id
			}
		}
	}
	if err := rows.Err(); err != nil {
		return contract.AuthorizationResult{}, fmt.Errorf("%w: iterate grants for evaluation: %w", ErrStorageUnavailable, err)
	}
	if smallestDeny != "" {
		result.Decision = contract.DecisionDeny
		result.GrantID = &smallestDeny
	} else if smallestAllow != "" {
		result.Decision = contract.DecisionAllow
		result.GrantID = &smallestAllow
	}
	return result, nil
}

type evaluationGrant struct {
	id           string
	effect       contract.GrantEffect
	serverID     string
	upstreamName sql.NullString
	constraint   *CompiledConstraint
	expiresAt    *time.Time
}

func loadEvaluationGrant(scanner grantScanner, compile func(string) (CompiledConstraint, error)) (evaluationGrant, error) {
	var (
		grant                          evaluationGrant
		principalID, effect, createdAt string
		constraintJSON, expiresAt      sql.NullString
	)
	if err := scanner.Scan(&grant.id, &principalID, &effect, &grant.serverID, &grant.upstreamName, &constraintJSON, &expiresAt, &createdAt); err != nil {
		return evaluationGrant{}, err
	}
	grant.effect = contract.GrantEffect(effect)
	if !validOpaqueID(grant.id) || !validOpaqueID(principalID) || !validGrantEffect(grant.effect) || !validOpaqueID(grant.serverID) ||
		grant.upstreamName.Valid && !validUpstreamName(grant.upstreamName.String) || !grant.upstreamName.Valid && constraintJSON.Valid {
		return evaluationGrant{}, ErrAuthorizationUnavailable
	}
	created, valid := canonicalTimestamp(createdAt)
	if !valid {
		return evaluationGrant{}, ErrAuthorizationUnavailable
	}
	if constraintJSON.Valid {
		compiled, err := compile(constraintJSON.String)
		if err != nil {
			return evaluationGrant{}, ErrAuthorizationUnavailable
		}
		grant.constraint = &compiled
	}
	if expiresAt.Valid {
		expiry, valid := canonicalTimestamp(expiresAt.String)
		if !valid || !expiry.After(created) {
			return evaluationGrant{}, ErrAuthorizationUnavailable
		}
		grant.expiresAt = &expiry
	}
	return grant, nil
}

func (grant evaluationGrant) applies(serverID, upstreamName string, evaluatedAt time.Time, arguments strictjson.Value, remainingRegexWork *int64) (bool, error) {
	if grant.serverID != serverID || grant.expiresAt != nil && !grant.expiresAt.After(evaluatedAt) ||
		grant.upstreamName.Valid && grant.upstreamName.String != upstreamName {
		return false, nil
	}
	if grant.constraint == nil {
		return true, nil
	}
	return constraintMatches(*grant.constraint, arguments, remainingRegexWork)
}

func constraintMatches(constraint CompiledConstraint, arguments strictjson.Value, remainingRegexWork *int64) (bool, error) {
	for _, atom := range constraint.atoms {
		actual, present := valueAtObjectPath(arguments, atom.segments)
		if !present {
			return false, nil
		}
		switch atom.operator {
		case ConstraintEquals:
			if !scalarValuesEqual(actual, atom.expected) {
				return false, nil
			}
		case ConstraintRegex:
			if actual.Type != strictjson.ValueString {
				return false, nil
			}
			if int64(len(actual.String)) > *remainingRegexWork {
				return false, ErrAuthorizationUnavailable
			}
			*remainingRegexWork -= int64(len(actual.String))
			if !atom.expression.MatchString(actual.String) {
				return false, nil
			}
		default:
			return false, ErrAuthorizationUnavailable
		}
	}
	return true, nil
}

func valueAtObjectPath(root strictjson.Value, segments []string) (strictjson.Value, bool) {
	current := root
	for _, segment := range segments {
		if current.Type != strictjson.ValueObject {
			return strictjson.Value{}, false
		}
		found := false
		for _, member := range current.Object {
			if member.Name == segment {
				current = member.Value
				found = true
				break
			}
		}
		if !found {
			return strictjson.Value{}, false
		}
	}
	return current, true
}

func scalarValuesEqual(actual, expected strictjson.Value) bool {
	if actual.Type != expected.Type {
		return false
	}
	switch expected.Type {
	case strictjson.ValueNull:
		return true
	case strictjson.ValueBoolean:
		return actual.Boolean == expected.Boolean
	case strictjson.ValueString:
		return actual.String == expected.String
	case strictjson.ValueNumber:
		return actual.Number == expected.Number
	default:
		return false
	}
}
