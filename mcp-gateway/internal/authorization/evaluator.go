package authorization

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
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
	result := contract.AuthorizationResult{
		Decision:    contract.DecisionBlock,
		EvaluatedAt: formatAuthorizationTime(evaluatedAt),
	}
	err = repository.view(ctx, func(transaction *sql.Tx) error {
		var revision int64
		if err := transaction.QueryRowContext(ctx, `SELECT revision FROM authorization_meta WHERE singleton = 1`).Scan(&revision); err != nil {
			return fmt.Errorf("read authorization revision for evaluation: %w", err)
		}
		if revision < 0 {
			return ErrAuthorizationUnavailable
		}
		result.AuthorizationRevision = strconv.FormatInt(revision, 10)

		rows, err := transaction.QueryContext(ctx, `
			SELECT id, principal_id, effect, server_id, upstream_name,
			       constraint_json, expires_at, created_at
			FROM grants
			WHERE principal_id = ?
			ORDER BY id
			LIMIT ?`, request.PrincipalID, mustLimit("grants")+1)
		if err != nil {
			return fmt.Errorf("read grants for evaluation: %w", err)
		}
		defer func() { _ = rows.Close() }()
		var smallestAllow, smallestDeny string
		count := int64(0)
		for rows.Next() {
			if count >= mustLimit("grants") {
				return ErrAuthorizationUnavailable
			}
			count++
			grant, loadErr := loadEvaluationGrant(rows)
			if loadErr != nil {
				return ErrAuthorizationUnavailable
			}
			if !grant.applies(request.ServerID, request.UpstreamName, evaluatedAt, arguments) {
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
			return fmt.Errorf("iterate grants for evaluation: %w", err)
		}
		if smallestDeny != "" {
			result.Decision = contract.DecisionDeny
			result.GrantID = &smallestDeny
		} else if smallestAllow != "" {
			result.Decision = contract.DecisionAllow
			result.GrantID = &smallestAllow
		}
		return nil
	})
	if err != nil {
		return contract.AuthorizationResult{}, err
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

func loadEvaluationGrant(scanner grantScanner) (evaluationGrant, error) {
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
		compiled, err := CompileConstraint([]byte(constraintJSON.String))
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

func (grant evaluationGrant) applies(serverID, upstreamName string, evaluatedAt time.Time, arguments strictjson.Value) bool {
	if grant.serverID != serverID || grant.expiresAt != nil && !grant.expiresAt.After(evaluatedAt) ||
		grant.upstreamName.Valid && grant.upstreamName.String != upstreamName {
		return false
	}
	return grant.constraint == nil || constraintMatches(*grant.constraint, arguments)
}

func constraintMatches(constraint CompiledConstraint, arguments strictjson.Value) bool {
	for _, atom := range constraint.atoms {
		actual, present := valueAtObjectPath(arguments, atom.segments)
		if !present || !scalarValuesEqual(actual, atom.expected) {
			return false
		}
	}
	return true
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
