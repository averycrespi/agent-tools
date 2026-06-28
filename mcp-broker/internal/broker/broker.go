package broker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
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
	ApprovalMode ApprovalMode
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

// Broker orchestrates the tool call pipeline.
type Broker struct {
	servers  ServerManager
	rules    *rules.Engine
	auditor  AuditLogger
	approver Approver
	logger   *slog.Logger
}

// New creates a Broker with the given components.
func New(servers ServerManager, rulesEngine *rules.Engine, auditor AuditLogger, approver Approver, logger *slog.Logger) *Broker {
	return &Broker{
		servers:  servers,
		rules:    rulesEngine,
		auditor:  auditor,
		approver: approver,
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

	// 1. Rules check
	verdict, ruleIndex := b.rules.EvaluateWithRule(tool, args)
	rec.Verdict = verdict.String()

	if b.logger != nil {
		b.logger.Debug("rules evaluated", "tool", tool, "verdict", verdict)
	}

	switch verdict {
	case rules.Deny:
		reason := "rule"
		if ruleIndex >= 0 {
			if configured := strings.TrimSpace(b.rules.Rules()[ruleIndex].Reason); configured != "" {
				reason = "rule: " + configured
			}
		}
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
