package audit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type causeKey struct{}

// Cause retains only safe attribution across asynchronous handoffs, never a
// request context, cancellation, credential secret, or session identifier.
type Cause struct {
	actor       contract.AuditActor
	initiator   *contract.AuditCredential
	correlation string
}

type cause = Cause

type durableCause struct {
	Correlation string                    `json:"correlation_id"`
	Initiator   *contract.AuditCredential `json:"initiator"`
}

func EncodeCause(ctx context.Context, fallbackID string) (string, error) {
	value := Capture(WithSystem(ctx))
	if value.correlation == "" {
		value.correlation = fallbackID
	}
	if !contract.ValidAuditID(value.correlation) || value.initiator != nil && !contract.ValidAuditCredential(value.initiator) {
		return "", ErrInvalidInput
	}
	encoded, err := json.Marshal(durableCause{Correlation: value.correlation, Initiator: value.initiator})
	return string(encoded), err
}

func DecodeCause(encoded string) (Cause, error) {
	var value durableCause
	if strictjson.Decode([]byte(encoded), &value, strictjson.Options{MaxBytes: contract.AuditDetailBytes, MaxDepth: 4, RejectUnknownMembers: true}) != nil || !contract.ValidAuditID(value.Correlation) || value.Initiator != nil && !contract.ValidAuditCredential(value.Initiator) {
		return Cause{}, ErrInvalidState
	}
	canonical, err := json.Marshal(value)
	if err != nil || string(canonical) != encoded {
		return Cause{}, ErrInvalidState
	}
	return Cause{actor: contract.AuditActor{Type: contract.AuditSystem}, initiator: value.Initiator, correlation: value.Correlation}, nil
}

func InheritCause(ctx context.Context, origin Cause) context.Context {
	current := Capture(ctx)
	if origin.correlation == "" || current.actor.Type == contract.AuditOperator || current.actor.Type == contract.AuditOffline || current.initiator != nil {
		return ctx
	}
	return WithCause(ctx, origin)
}

func Capture(ctx context.Context) Cause {
	current, _ := ctx.Value(causeKey{}).(cause)
	return current
}

func WithCause(ctx context.Context, value Cause) context.Context {
	return context.WithValue(ctx, causeKey{}, value)
}

func WithCorrelation(ctx context.Context, correlation string) context.Context {
	current := Capture(ctx)
	current.correlation = correlation
	return WithCause(ctx, current)
}

func WithOperator(ctx context.Context, credential contract.AuditCredential, correlation string) context.Context {
	if correlation == "" {
		current, _ := ctx.Value(causeKey{}).(cause)
		correlation = current.correlation
	}
	return context.WithValue(ctx, causeKey{}, cause{actor: contract.AuditActor{Type: contract.AuditOperator, Credential: &credential}, correlation: correlation})
}

func WithOffline(ctx context.Context) context.Context {
	current, _ := ctx.Value(causeKey{}).(cause)
	return context.WithValue(ctx, causeKey{}, cause{actor: contract.AuditActor{Type: contract.AuditOffline}, correlation: current.correlation})
}

func WithSystem(ctx context.Context) context.Context {
	current, _ := ctx.Value(causeKey{}).(cause)
	if current.actor.Type == contract.AuditOperator {
		current.initiator = current.actor.Credential
	}
	current.actor = contract.AuditActor{Type: contract.AuditSystem}
	return context.WithValue(ctx, causeKey{}, current)
}

func NewAttempt(ctx context.Context, now time.Time, category, action string, target contract.AuditTarget) (contract.AuditEvent, error) {
	id, err := newID(now)
	if err != nil {
		return contract.AuditEvent{}, err
	}
	current, _ := ctx.Value(causeKey{}).(cause)
	if current.actor.Type == "" {
		current.actor.Type = contract.AuditSystem
	}
	if current.correlation == "" {
		current.correlation = id
	}
	event := contract.AuditEvent{AuditSummary: contract.AuditSummary{
		ID: id, Timestamp: now.UTC().Format(contract.AuditTimestampLayout), Category: category, Action: action,
		Phase: "attempt", Outcome: "pending", Actor: current.actor, Initiator: current.initiator,
		CorrelationID: current.correlation, Target: target,
	}}
	check := event
	check.Sequence = "1"
	if contract.ValidateAuditEvent(check) != nil {
		return contract.AuditEvent{}, ErrInvalidInput
	}
	return event, nil
}

func Outcome(attempt contract.AuditEvent, now time.Time, outcome string) (contract.AuditEvent, error) {
	id, err := newID(now)
	if err != nil {
		return contract.AuditEvent{}, err
	}
	event := attempt
	event.ID, event.Sequence, event.Timestamp = id, "", now.UTC().Format(contract.AuditTimestampLayout)
	event.Phase, event.Outcome = "outcome", outcome
	check := event
	check.Sequence = "1"
	if contract.ValidateAuditEvent(check) != nil {
		return contract.AuditEvent{}, ErrInvalidInput
	}
	return event, nil
}

// MutationTx records a completed SQLite change in the caller's transaction.
// External effects require a separately durable attempt before they begin.
func MutationTx(ctx context.Context, tx *sql.Tx, now time.Time, category, action string, target contract.AuditTarget) error {
	return MutationOutcomeTx(ctx, tx, now, category, action, target, "succeeded", contract.AuditDetail{})
}

func (repository *Repository) RecordProblem(ctx context.Context, now time.Time, category, action string, target contract.AuditTarget, code contract.ProblemCode) error {
	problem, ok := contract.ProblemForCode(code)
	if !ok {
		return ErrInvalidInput
	}
	outcome := "rejected"
	if problem.Status >= 500 {
		outcome = "unknown"
	}
	value := string(code)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), contract.SQLiteBusyDeadline)
	defer cancel()
	return repository.store.Mutate(ctx, func(tx *sql.Tx) error {
		return MutationOutcomeTx(ctx, tx, now, category, action, target, outcome, contract.AuditDetail{Problem: &value})
	})
}

func MutationOutcomeTx(ctx context.Context, tx *sql.Tx, now time.Time, category, action string, target contract.AuditTarget, result string, detail contract.AuditDetail) error {
	attempt, err := NewAttempt(ctx, now, category, action, target)
	if err != nil {
		return err
	}
	if _, err := AppendTx(ctx, tx, attempt); err != nil {
		return err
	}
	outcome, err := Outcome(attempt, now, result)
	if err != nil {
		return err
	}
	outcome.Detail = detail
	_, err = AppendTx(ctx, tx, outcome)
	return err
}

func Finish(ctx context.Context, store Store, attempt contract.AuditEvent, now time.Time, outcome string) error {
	// Cancellation may follow the external effect; settlement still needs a
	// bounded chance to record what happened without reusing a cancelled tx.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), contract.SQLiteBusyDeadline)
	defer cancel()
	event, err := Outcome(attempt, now, outcome)
	if err != nil {
		return err
	}
	return Append(ctx, store, event)
}

func Append(ctx context.Context, store Store, event contract.AuditEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return store.Mutate(ctx, func(tx *sql.Tx) error { _, err := AppendTx(ctx, tx, event); return err })
}

func newID(now time.Time) (string, error) {
	milliseconds := now.UnixMilli()
	if milliseconds < 0 || milliseconds > 1<<48-1 {
		return "", ErrInvalidInput
	}
	var value [16]byte
	for index := 5; index >= 0; index-- {
		value[index] = byte(milliseconds & 255)
		milliseconds >>= 8
	}
	if _, err := rand.Read(value[6:]); err != nil {
		return "", fmt.Errorf("generate audit identity: %w", err)
	}
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var encoded [26]byte
	for character := range encoded {
		var bits byte
		for bit := range 5 {
			sourceBit := character*5 + bit - 2
			bits <<= 1
			if sourceBit >= 0 {
				bits |= (value[sourceBit/8] >> (7 - sourceBit%8)) & 1
			}
		}
		encoded[character] = alphabet[bits]
	}
	return string(encoded[:]), nil
}
