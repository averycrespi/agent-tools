package broker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

var ErrDenied = errors.New("denied by policy")

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
// denials, "timeout" for timeouts, and "" when approved or not applicable.
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

// Handle drives the full tool call pipeline: rules -> approval -> proxy -> audit.
func (b *Broker) Handle(ctx context.Context, tool string, args map[string]any) (any, error) {
	rec := audit.Record{
		Timestamp: time.Now(),
		Tool:      tool,
		Args:      args,
	}

	// 1. Rules check
	verdict := b.rules.Evaluate(tool)
	rec.Verdict = verdict.String()

	if b.logger != nil {
		b.logger.Debug("rules evaluated", "tool", tool, "verdict", verdict)
	}

	switch verdict {
	case rules.Deny:
		rec.Error = fmt.Sprintf("denied by policy: %s", tool)
		_ = b.auditor.Record(ctx, rec)
		return nil, fmt.Errorf("%w: %s", ErrDenied, tool)

	case rules.RequireApproval:
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
			rec.Error = fmt.Sprintf("denied by approver: %s", tool)
			_ = b.auditor.Record(ctx, rec)
			return nil, fmt.Errorf("%w (by approver): %s", ErrDenied, tool)
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

	return result.Content, nil
}

// Tools returns all discovered tools (delegates to server manager).
func (b *Broker) Tools() []server.Tool {
	return b.servers.Tools()
}
