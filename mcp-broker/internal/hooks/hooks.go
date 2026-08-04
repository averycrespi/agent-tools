package hooks

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/broker"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

const EventRequireApproval = "require-approval"

// Handler is a startup-resolved direct command handler.
type Handler struct {
	Command string
	Args    []string
	Env     []string
	Timeout time.Duration
}

// RunStatus classifies command outcomes without exposing subprocess details.
type RunStatus string

const (
	RunStatusSucceeded RunStatus = "succeeded"
	RunStatusFailed    RunStatus = "failed"
	RunStatusTimedOut  RunStatus = "timed-out"
	RunStatusCanceled  RunStatus = "canceled"
)

type runner interface {
	Run(ctx context.Context, handler Handler, payload []byte) RunStatus
}

type job struct {
	handlerIndex int
	handler      Handler
	requestID    string
	payload      []byte
	deadline     time.Time
}

// Dispatcher serializes approval events and makes bounded non-blocking command admissions.
type Dispatcher struct {
	ctx        context.Context
	cancel     context.CancelFunc
	handlers   []Handler
	maxPayload int64
	maxBytes   int64
	queue      chan job
	runner     runner
	logger     *slog.Logger

	mu          sync.Mutex
	accepting   bool
	queuedBytes int64
	wg          sync.WaitGroup
	workersDone chan struct{}
	closeOnce   sync.Once
}

type eventPayload struct {
	SchemaVersion int                   `json:"schema_version"`
	EventName     string                `json:"hook_event_name"`
	RequestID     string                `json:"request_id"`
	OccurredAt    time.Time             `json:"occurred_at"`
	ToolName      string                `json:"tool_name"`
	ToolInput     map[string]any        `json:"tool_input"`
	Policy        broker.ApprovalPolicy `json:"policy"`
	Grant         *approvalGrantPayload `json:"grant,omitempty"`
}

type approvalGrantPayload struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"`
}

// New constructs a dispatcher and expands handler environment references once.
func New(lifetime context.Context, cfg config.HooksConfig, logger *slog.Logger) *Dispatcher {
	return newWithRunner(lifetime, cfg, logger, commandRunner{})
}

func newWithRunner(lifetime context.Context, cfg config.HooksConfig, logger *slog.Logger, commandRunner runner) *Dispatcher {
	ctx, cancel := context.WithCancel(lifetime)
	d := &Dispatcher{
		ctx: ctx, cancel: cancel, handlers: runtimeHandlers(cfg.Events.RequireApproval),
		maxPayload: cfg.Dispatch.MaxPayloadBytes, maxBytes: cfg.Dispatch.MaxQueuedBytes,
		queue: make(chan job, cfg.Dispatch.QueueSize), runner: commandRunner, logger: logger,
		accepting: true, workersDone: make(chan struct{}),
	}
	if len(d.handlers) > 0 {
		for range cfg.Dispatch.MaxConcurrent {
			d.wg.Add(1)
			go d.worker()
		}
	}
	go func() {
		d.wg.Wait()
		close(d.workersDone)
	}()
	return d
}

func runtimeHandlers(configured []config.HookHandlerConfig) []Handler {
	handlers := make([]Handler, 0, len(configured))
	for _, handler := range configured {
		env := environmentWithOverlay(handler.Env)
		handlers = append(handlers, Handler{
			Command: handler.Command,
			Args:    append([]string(nil), handler.Args...),
			Env:     env,
			Timeout: time.Duration(handler.TimeoutSeconds) * time.Second,
		})
	}
	return handlers
}

func environmentWithOverlay(overlay map[string]string) []string {
	env := make(map[string]string, len(os.Environ())+len(overlay))
	for _, entry := range os.Environ() {
		for i := 0; i < len(entry); i++ {
			if entry[i] == '=' {
				env[entry[:i]] = entry[i+1:]
				break
			}
		}
	}
	for key, value := range overlay {
		env[key] = os.ExpandEnv(value)
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+env[key])
	}
	return result
}

// Observe attempts one non-blocking admission per configured handler.
func (d *Dispatcher) Observe(request broker.ApprovalRequest) {
	if len(d.handlers) == 0 || !d.isAccepting() {
		return
	}
	payload, err := serialize(request)
	if err != nil {
		d.logDrop(-1, request.ID, "serialization")
		return
	}
	if int64(len(payload)) > d.maxPayload {
		d.logDrop(-1, request.ID, "oversized-payload")
		return
	}

	for i, handler := range d.handlers {
		d.tryAdmit(job{
			handlerIndex: i, handler: handler, requestID: request.ID, payload: payload,
			deadline: time.Now().Add(handler.Timeout),
		})
	}
}

func serialize(request broker.ApprovalRequest) ([]byte, error) {
	input := request.ToolInput
	if input == nil {
		input = map[string]any{}
	}
	payload := eventPayload{
		SchemaVersion: 1,
		EventName:     EventRequireApproval,
		RequestID:     request.ID,
		OccurredAt:    request.OccurredAt,
		ToolName:      request.ToolName,
		ToolInput:     input,
		Policy:        request.Policy,
	}
	if request.Grant != nil {
		payload.Grant = &approvalGrantPayload{
			ID: request.Grant.ID, Name: request.Grant.Name,
			Fingerprint: request.Grant.Fingerprint, Status: request.Grant.Status,
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func (d *Dispatcher) isAccepting() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.accepting && d.ctx.Err() == nil
}

func (d *Dispatcher) tryAdmit(candidate job) {
	size := int64(len(candidate.payload))
	dropReason := ""
	d.mu.Lock()
	switch {
	case !d.accepting || d.ctx.Err() != nil:
		dropReason = "shutdown"
	case d.queuedBytes+size > d.maxBytes:
		dropReason = "byte-budget"
	default:
		select {
		case d.queue <- candidate:
			d.queuedBytes += size
		default:
			dropReason = "queue-full"
		}
	}
	d.mu.Unlock()
	if dropReason != "" {
		d.logDrop(candidate.handlerIndex, candidate.requestID, dropReason)
	}
}

func (d *Dispatcher) worker() {
	defer d.wg.Done()
	for {
		select {
		case <-d.ctx.Done():
			return
		case queued := <-d.queue:
			d.releaseReservation(queued.payload)
			if d.ctx.Err() != nil {
				d.logDrop(queued.handlerIndex, queued.requestID, "shutdown")
				continue
			}
			if !time.Now().Before(queued.deadline) {
				d.logDrop(queued.handlerIndex, queued.requestID, "expired")
				continue
			}
			d.run(queued)
		}
	}
}

func (d *Dispatcher) run(queued job) {
	ctx, cancel := context.WithDeadline(d.ctx, queued.deadline)
	defer cancel()
	if ctx.Err() != nil {
		d.logDrop(queued.handlerIndex, queued.requestID, "expired")
		return
	}
	started := time.Now()
	status := d.runner.Run(ctx, queued.handler, queued.payload)
	if d.logger != nil {
		d.logger.Info("hook command completed",
			"event", EventRequireApproval,
			"handler_index", queued.handlerIndex,
			"request_id", queued.requestID,
			"duration", time.Since(started),
			"status", status,
		)
	}
}

func (d *Dispatcher) releaseReservation(payload []byte) {
	d.mu.Lock()
	d.queuedBytes -= int64(len(payload))
	d.mu.Unlock()
}

func (d *Dispatcher) logDrop(handlerIndex int, requestID, reason string) {
	if d.logger == nil {
		return
	}
	d.logger.Warn("hook command dropped",
		"event", EventRequireApproval,
		"handler_index", handlerIndex,
		"request_id", requestID,
		"status", "dropped",
		"drop_reason", reason,
	)
}

// Close rejects new work, cancels running commands, discards queued jobs, and waits within ctx.
func (d *Dispatcher) Close(ctx context.Context) error {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.accepting = false
		d.cancel()
		d.mu.Unlock()
		d.discardQueued()
	})

	select {
	case <-d.workersDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Dispatcher) discardQueued() {
	for {
		select {
		case queued := <-d.queue:
			d.releaseReservation(queued.payload)
			d.logDrop(queued.handlerIndex, queued.requestID, "shutdown")
		default:
			return
		}
	}
}
