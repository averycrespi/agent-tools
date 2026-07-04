package broker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/grants"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

var ErrDenied = errors.New("denied by policy")

// ApprovalMode controls what happens when a rule requires human approval.
type ApprovalMode string

const (
	// ApprovalModeWait is the default behavior: wait for a human approver.
	ApprovalModeWait ApprovalMode = ""
	// ApprovalModeReject rejects immediately instead of requesting approval.
	ApprovalModeReject ApprovalMode = "reject"
)

// HandleOptions contains per-call broker behavior options.
type HandleOptions struct {
	ApprovalMode     ApprovalMode
	GrantToken       string
	GrantHeaderError string
}

// ServerManager proxies tool calls to backend MCP servers.
type ServerManager interface {
	Tools() []server.Tool
	Call(ctx context.Context, tool string, args map[string]any) (*server.ToolResult, error)
}

// AuditLogger records audit entries.
type AuditLogger interface {
	Record(ctx context.Context, rec audit.Record) error
	Query(ctx context.Context, opts audit.QueryOpts) ([]audit.Record, int, error)
}

// Approver handles human approval decisions.
// It returns (approved, denialReason, err). denialReason is "user" for explicit
// denials, "user: <reason>" for explicit denials with a reason, "timeout" for
// timeouts, and "" when approved or not applicable.
type Approver interface {
	Review(ctx context.Context, tool string, args map[string]any) (bool, string, error)
}

// RuleEvaluator evaluates policy and returns matched metadata from the same rules snapshot.
type RuleEvaluator interface {
	EvaluateWithMetadata(tool string, args map[string]any) rules.Evaluation
}

// GrantValidator validates request-supplied grant tokens against durable grant state.
type GrantValidator interface {
	ValidateToken(ctx context.Context, token string, now time.Time) (grants.Grant, error)
}

// Broker orchestrates the tool call pipeline.
type Broker struct {
	servers  ServerManager
	rules    RuleEvaluator
	auditor  AuditLogger
	approver Approver
	grants   GrantValidator
	logger   *slog.Logger
}

// New creates a Broker with the given components.
func New(servers ServerManager, rulesEngine RuleEvaluator, auditor AuditLogger, approver Approver, logger *slog.Logger) *Broker {
	return NewWithGrants(servers, rulesEngine, auditor, approver, nil, logger)
}

// NewWithGrants creates a Broker with durable grant validation enabled.
func NewWithGrants(servers ServerManager, rulesEngine RuleEvaluator, auditor AuditLogger, approver Approver, grantValidator GrantValidator, logger *slog.Logger) *Broker {
	return &Broker{
		servers:  servers,
		rules:    rulesEngine,
		auditor:  auditor,
		approver: approver,
		grants:   grantValidator,
		logger:   logger,
	}
}

// Handle drives the full tool call pipeline and returns the result content.
func (b *Broker) Handle(ctx context.Context, tool string, args map[string]any) (any, error) {
	result, err := b.HandleToolResult(ctx, tool, args)
	if err != nil {
		return nil, err
	}
	return result.Content, nil
}

// HandleToolResult drives the full tool call pipeline: rules -> approval -> proxy -> audit.
func (b *Broker) HandleToolResult(ctx context.Context, tool string, args map[string]any) (*server.ToolResult, error) {
	return b.HandleToolResultWithOptions(ctx, tool, args, HandleOptions{})
}

// HandleToolResultWithOptions drives the full tool call pipeline with per-call options.
func (b *Broker) HandleToolResultWithOptions(ctx context.Context, tool string, args map[string]any, opts HandleOptions) (*server.ToolResult, error) {
	rec := audit.Record{
		Timestamp: time.Now(),
		Tool:      tool,
		Args:      args,
	}

	// 1. Grants/rules check
	evaluation, verdict, err := b.evaluatePolicy(ctx, tool, args, opts, &rec)
	if err != nil {
		_ = b.auditor.Record(ctx, rec)
		return nil, fmt.Errorf("%w: %s", ErrDenied, rec.Error)
	}
	rec.Verdict = verdict.String()

	if b.logger != nil {
		b.logger.Debug("rules evaluated", "tool", tool, "verdict", verdict, "rule_source", rec.RuleSource)
	}

	switch verdict {
	case rules.Deny:
		reason := ruleDenialReason(rec.RuleSource, evaluation)
		rec.DenialReason = reason
		rec.Error = formatDenialMessage(reason)
		_ = b.auditor.Record(ctx, rec)
		return nil, fmt.Errorf("%w: %s", ErrDenied, rec.Error)

	case rules.RequireApproval:
		if opts.ApprovalMode == ApprovalModeReject {
			approved := false
			rec.Approved = &approved
			rec.DenialReason = "approval-mode: reject"
			rec.Error = formatApprovalModeRejectMessage(tool)
			_ = b.auditor.Record(ctx, rec)
			return nil, fmt.Errorf("%w: %s", ErrDenied, rec.Error)
		}

		if b.approver == nil {
			rec.Error = "no approver configured"
			_ = b.auditor.Record(ctx, rec)
			return nil, fmt.Errorf("approval required but no approver configured for: %s", tool)
		}

		approved, denialReason, err := b.approver.Review(ctx, tool, args)
		rec.Approved = &approved
		rec.DenialReason = denialReason
		if err != nil {
			rec.Error = fmt.Sprintf("approver error: %v", err)
			_ = b.auditor.Record(ctx, rec)
			return nil, fmt.Errorf("approver error for %s: %w", tool, err)
		}
		if !approved {
			rec.Error = formatDenialMessage(denialReason)
			_ = b.auditor.Record(ctx, rec)
			return nil, fmt.Errorf("%w: %s", ErrDenied, rec.Error)
		}

	case rules.Allow:
		// proceed
	}

	// 2. Proxy to backend
	result, err := b.servers.Call(ctx, tool, args)
	if err != nil {
		rec.Error = err.Error()
		_ = b.auditor.Record(ctx, rec)
		return nil, fmt.Errorf("backend error for %s: %w", tool, err)
	}

	if result.IsError {
		rec.Error = fmt.Sprintf("%v", result.Content)
	}

	// 3. Audit
	_ = b.auditor.Record(ctx, rec)

	if b.logger != nil {
		b.logger.Info("tool call handled", "tool", tool, "verdict", verdict)
	}

	return result, nil
}

func (b *Broker) evaluatePolicy(ctx context.Context, tool string, args map[string]any, opts HandleOptions, rec *audit.Record) (rules.Evaluation, rules.Verdict, error) {
	if opts.GrantHeaderError != "" {
		rec.Verdict = rules.Deny.String()
		rec.GrantStatus = "invalid"
		rec.RuleSource = "none/default"
		rec.DenialReason = "grant: invalid"
		rec.Error = "invalid grant header: " + opts.GrantHeaderError
		return rules.Evaluation{}, rules.Deny, ErrDenied
	}

	if opts.GrantToken != "" {
		if b.grants == nil {
			rec.Verdict = rules.Deny.String()
			rec.GrantStatus = "store-error"
			rec.RuleSource = "none/default"
			rec.DenialReason = "grant: unavailable"
			rec.Error = "grant validation unavailable"
			return rules.Evaluation{}, rules.Deny, ErrDenied
		}

		grant, err := b.grants.ValidateToken(ctx, opts.GrantToken, time.Now())
		if err != nil {
			populateGrantRecord(rec, grant)
			rec.Verdict = rules.Deny.String()
			rec.GrantStatus = grantStatusForError(err)
			rec.RuleSource = "none/default"
			rec.DenialReason = "grant: " + rec.GrantStatus
			rec.Error = grantErrorMessage(err)
			return rules.Evaluation{}, rules.Deny, ErrDenied
		}
		populateGrantRecord(rec, grant)

		grantEngine, err := grant.Engine()
		if err != nil {
			rec.Verdict = rules.Deny.String()
			rec.RuleSource = "grant"
			rec.GrantStatus = "invalid"
			rec.DenialReason = "grant: invalid rules"
			rec.Error = "grant rules invalid"
			return rules.Evaluation{}, rules.Deny, ErrDenied
		}
		grantEvaluation := grantEngine.EvaluateWithMetadata(tool, args)
		if grantEvaluation.Matched {
			rec.RuleSource = "grant"
			return grantEvaluation, grantEvaluation.Verdict, nil
		}
	}

	baseEvaluation := b.rules.EvaluateWithMetadata(tool, args)
	if baseEvaluation.Matched {
		rec.RuleSource = "base"
	} else {
		rec.RuleSource = "none/default"
	}
	return baseEvaluation, baseEvaluation.Verdict, nil
}

func populateGrantRecord(rec *audit.Record, grant grants.Grant) {
	if grant.ID == "" {
		return
	}
	rec.GrantID = grant.ID
	rec.GrantName = grant.Name
	rec.GrantFingerprint = grant.Fingerprint
	rec.GrantStatus = grant.Status
}

func grantStatusForError(err error) string {
	switch {
	case errors.Is(err, grants.ErrExpired):
		return "expired"
	case errors.Is(err, grants.ErrRevoked):
		return "revoked"
	case errors.Is(err, grants.ErrUnknown):
		return "invalid"
	default:
		return "store-error"
	}
}

func grantErrorMessage(err error) string {
	switch {
	case errors.Is(err, grants.ErrExpired):
		return "grant expired"
	case errors.Is(err, grants.ErrRevoked):
		return "grant revoked"
	case errors.Is(err, grants.ErrUnknown):
		return "invalid grant token"
	default:
		return "grant validation failed"
	}
}

func ruleDenialReason(source string, evaluation rules.Evaluation) string {
	reason := "rule"
	if source == "grant" {
		reason = "grant rule"
	}
	if evaluation.Matched {
		if configured := strings.TrimSpace(evaluation.RuleReason); configured != "" {
			reason += ": " + configured
		}
	}
	return reason
}

func formatDenialMessage(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "user"
	}
	return "denied by " + reason
}

func formatApprovalModeRejectMessage(tool string) string {
	return fmt.Sprintf("tool call blocked: approval is required for %s, but this request uses Mcp-Broker-Approval-Mode=reject", tool)
}

// Tools returns all discovered tools (delegates to server manager).
func (b *Broker) Tools() []server.Tool {
	return b.servers.Tools()
}
