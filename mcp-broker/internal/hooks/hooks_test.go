package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/broker"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

type capturedRun struct {
	handler Handler
	payload []byte
}

type captureRunner struct {
	mu      sync.Mutex
	runs    []capturedRun
	started chan struct{}
	release chan struct{}
}

func (r *captureRunner) Run(ctx context.Context, handler Handler, payload []byte) RunStatus {
	r.mu.Lock()
	r.runs = append(r.runs, capturedRun{handler: handler, payload: append([]byte(nil), payload...)})
	first := len(r.runs) == 1
	r.mu.Unlock()
	if first && r.started != nil {
		close(r.started)
	}
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return RunStatusCanceled
		}
	}
	return RunStatusSucceeded
}

func (r *captureRunner) snapshot() []capturedRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]capturedRun(nil), r.runs...)
}

type concurrencyRunner struct {
	mu        sync.Mutex
	active    int
	maxActive int
	started   chan struct{}
	release   chan struct{}
}

func (r *concurrencyRunner) Run(context.Context, Handler, []byte) RunStatus {
	r.mu.Lock()
	r.active++
	if r.active > r.maxActive {
		r.maxActive = r.active
	}
	if r.active == 2 {
		close(r.started)
	}
	r.mu.Unlock()
	<-r.release
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	return RunStatusSucceeded
}

func (r *concurrencyRunner) maximum() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxActive
}

type stubbornRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r *stubbornRunner) Run(context.Context, Handler, []byte) RunStatus {
	close(r.started)
	<-r.release
	return RunStatusCanceled
}

func hookConfig(handlers int) config.HooksConfig {
	cfg := config.DefaultConfig().Hooks
	cfg.Dispatch.MaxConcurrent = 1
	for range handlers {
		cfg.Events.RequireApproval = append(cfg.Events.RequireApproval, config.HookHandlerConfig{
			Command: "notify", TimeoutSeconds: 10,
		})
	}
	return cfg
}

func approvalRequest(input map[string]any) broker.ApprovalRequest {
	return broker.ApprovalRequest{
		ID:         "c9cb427bc1387a23c9cb427bc1387a23",
		OccurredAt: time.Date(2026, 8, 3, 18, 42, 0, 0, time.UTC),
		ToolName:   "github.create_pull_request",
		ToolInput:  input,
		Policy:     broker.ApprovalPolicy{Verdict: "require-approval", RuleSource: "base"},
	}
}

func TestDispatcherWritesVersionedJSONWithTrailingNewline(t *testing.T) {
	runner := &captureRunner{}
	d := newWithRunner(context.Background(), hookConfig(1), nil, runner)
	t.Cleanup(func() { require.NoError(t, d.Close(context.Background())) })

	d.Observe(approvalRequest(map[string]any{"owner": "example", "nested": map[string]any{"enabled": true}}))
	require.Eventually(t, func() bool { return len(runner.snapshot()) == 1 }, time.Second, time.Millisecond)

	payload := runner.snapshot()[0].payload
	require.Equal(t, byte('\n'), payload[len(payload)-1])
	var decoded struct {
		SchemaVersion int            `json:"schema_version"`
		EventName     string         `json:"hook_event_name"`
		RequestID     string         `json:"request_id"`
		OccurredAt    time.Time      `json:"occurred_at"`
		ToolName      string         `json:"tool_name"`
		ToolInput     map[string]any `json:"tool_input"`
		Policy        struct {
			Verdict    string `json:"verdict"`
			RuleSource string `json:"rule_source"`
		} `json:"policy"`
		Grant *broker.ApprovalGrant `json:"grant"`
	}
	require.NoError(t, json.Unmarshal(payload, &decoded))
	require.Equal(t, 1, decoded.SchemaVersion)
	require.Equal(t, EventRequireApproval, decoded.EventName)
	require.Equal(t, "c9cb427bc1387a23c9cb427bc1387a23", decoded.RequestID)
	require.Equal(t, "github.create_pull_request", decoded.ToolName)
	require.Equal(t, map[string]any{"owner": "example", "nested": map[string]any{"enabled": true}}, decoded.ToolInput)
	require.Equal(t, "require-approval", decoded.Policy.Verdict)
	require.Equal(t, "base", decoded.Policy.RuleSource)
	require.Nil(t, decoded.Grant)
}

func TestDispatcherNormalizesNilInputAndIncludesGrantMetadata(t *testing.T) {
	runner := &captureRunner{}
	d := newWithRunner(context.Background(), hookConfig(1), nil, runner)
	t.Cleanup(func() { require.NoError(t, d.Close(context.Background())) })

	request := approvalRequest(nil)
	request.Policy.RuleSource = "grant"
	request.Grant = &broker.ApprovalGrant{ID: "grant-1", Name: "release", Fingerprint: "abc123", Status: "active"}
	d.Observe(request)
	require.Eventually(t, func() bool { return len(runner.snapshot()) == 1 }, time.Second, time.Millisecond)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(runner.snapshot()[0].payload, &decoded))
	require.Equal(t, map[string]any{}, decoded["tool_input"])
	require.Equal(t, map[string]any{"id": "grant-1", "name": "release", "fingerprint": "abc123", "status": "active"}, decoded["grant"])
}

func TestDispatcherSnapshotsInputBeforeAsynchronousExecution(t *testing.T) {
	runner := &captureRunner{started: make(chan struct{}), release: make(chan struct{})}
	d := newWithRunner(context.Background(), hookConfig(1), nil, runner)
	t.Cleanup(func() { require.NoError(t, d.Close(context.Background())) })

	d.Observe(approvalRequest(map[string]any{"call": "blocker"}))
	<-runner.started
	input := map[string]any{"nested": map[string]any{"value": "before"}}
	d.Observe(approvalRequest(input))
	input["nested"].(map[string]any)["value"] = "after"
	close(runner.release)
	require.Eventually(t, func() bool { return len(runner.snapshot()) == 2 }, time.Second, time.Millisecond)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(runner.snapshot()[1].payload, &decoded))
	require.Equal(t, map[string]any{"nested": map[string]any{"value": "before"}}, decoded["tool_input"])
}

func TestDispatcherDropsUnserializableAndOversizedPayloadsWithoutRunning(t *testing.T) {
	runner := &captureRunner{}
	cfg := hookConfig(1)
	cfg.Dispatch.MaxPayloadBytes = 200
	cfg.Dispatch.MaxQueuedBytes = 200
	d := newWithRunner(context.Background(), cfg, nil, runner)
	t.Cleanup(func() { require.NoError(t, d.Close(context.Background())) })

	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	d.Observe(approvalRequest(cyclic))
	d.Observe(approvalRequest(map[string]any{"large": string(make([]byte, 500))}))
	time.Sleep(10 * time.Millisecond)
	require.Empty(t, runner.snapshot())
}

func TestDispatcherAdmissionIsNonBlockingAndDropsEachSaturatedHandler(t *testing.T) {
	runner := &captureRunner{started: make(chan struct{}), release: make(chan struct{})}
	cfg := hookConfig(2)
	cfg.Dispatch.QueueSize = 1
	d := newWithRunner(context.Background(), cfg, nil, runner)
	t.Cleanup(func() { require.NoError(t, d.Close(context.Background())) })

	d.Observe(approvalRequest(map[string]any{"call": 1}))
	<-runner.started
	done := make(chan struct{})
	go func() {
		d.Observe(approvalRequest(map[string]any{"call": 2}))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Observe waited for queue capacity")
	}
	close(runner.release)
	require.Eventually(t, func() bool { return len(runner.snapshot()) == 2 }, time.Second, time.Millisecond)
}

func TestDispatcherExpandsEnvironmentOnceAtConstruction(t *testing.T) {
	t.Setenv("HOOK_CHANNEL", "startup")
	runner := &captureRunner{}
	cfg := hookConfig(1)
	cfg.Events.RequireApproval[0].Env = map[string]string{"CHANNEL": "$HOOK_CHANNEL", "COMBINED": "${HOOK_CHANNEL}-suffix"}
	d := newWithRunner(context.Background(), cfg, nil, runner)
	t.Cleanup(func() { require.NoError(t, d.Close(context.Background())) })
	_ = os.Setenv("HOOK_CHANNEL", "changed")

	d.Observe(approvalRequest(nil))
	require.Eventually(t, func() bool { return len(runner.snapshot()) == 1 }, time.Second, time.Millisecond)
	env := runner.snapshot()[0].handler.Env
	require.Contains(t, env, "CHANNEL=startup")
	require.Contains(t, env, "COMBINED=startup-suffix")
}

func TestDispatcherAcceptsHandlersIndependentlyUnderSaturation(t *testing.T) {
	runner := &captureRunner{started: make(chan struct{}), release: make(chan struct{})}
	cfg := hookConfig(1)
	cfg.Dispatch.QueueSize = 1
	d := newWithRunner(context.Background(), cfg, nil, runner)
	t.Cleanup(func() { require.NoError(t, d.Close(context.Background())) })

	d.Observe(approvalRequest(map[string]any{"call": "blocker"}))
	<-runner.started
	d.handlers = append(d.handlers, d.handlers[0])
	d.Observe(approvalRequest(map[string]any{"call": "partial"}))
	close(runner.release)
	require.Eventually(t, func() bool { return len(runner.snapshot()) == 2 }, time.Second, time.Millisecond)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(runner.snapshot()[1].payload, &decoded))
	require.Equal(t, "partial", decoded["tool_input"].(map[string]any)["call"])
}

func TestDispatcherEnforcesQueuedByteBudget(t *testing.T) {
	runner := &captureRunner{started: make(chan struct{}), release: make(chan struct{})}
	cfg := hookConfig(1)
	payload, err := serialize(approvalRequest(map[string]any{"call": 2}))
	require.NoError(t, err)
	cfg.Dispatch.QueueSize = 2
	cfg.Dispatch.MaxQueuedBytes = int64(len(payload))
	d := newWithRunner(context.Background(), cfg, nil, runner)
	t.Cleanup(func() { require.NoError(t, d.Close(context.Background())) })

	d.Observe(approvalRequest(map[string]any{"call": 1}))
	<-runner.started
	d.Observe(approvalRequest(map[string]any{"call": 2}))
	d.Observe(approvalRequest(map[string]any{"call": 3}))
	close(runner.release)
	require.Eventually(t, func() bool { return len(runner.snapshot()) == 2 }, time.Second, time.Millisecond)

	d.Observe(approvalRequest(map[string]any{"call": 4}))
	require.Eventually(t, func() bool { return len(runner.snapshot()) == 3 }, time.Second, time.Millisecond)
}

func TestDispatcherEnforcesConcurrencyLimit(t *testing.T) {
	runner := &concurrencyRunner{started: make(chan struct{}), release: make(chan struct{})}
	cfg := hookConfig(3)
	cfg.Dispatch.MaxConcurrent = 2
	d := newWithRunner(context.Background(), cfg, nil, runner)
	t.Cleanup(func() { require.NoError(t, d.Close(context.Background())) })

	d.Observe(approvalRequest(nil))
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("two workers did not start")
	}
	require.Equal(t, 2, runner.maximum())
	close(runner.release)
}

func TestDispatcherDropsQueuedWorkThatExpiresBeforeStart(t *testing.T) {
	runner := &captureRunner{started: make(chan struct{}), release: make(chan struct{})}
	cfg := hookConfig(1)
	d := newWithRunner(context.Background(), cfg, nil, runner)
	d.handlers[0].Timeout = 20 * time.Millisecond
	t.Cleanup(func() { require.NoError(t, d.Close(context.Background())) })

	d.Observe(approvalRequest(map[string]any{"call": 1}))
	<-runner.started
	d.Observe(approvalRequest(map[string]any{"call": 2}))
	time.Sleep(40 * time.Millisecond)
	close(runner.release)
	time.Sleep(10 * time.Millisecond)
	require.Len(t, runner.snapshot(), 1)
}

func TestDispatcherLogsContainMetadataOnly(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	runner := &captureRunner{}
	cfg := hookConfig(1)
	cfg.Events.RequireApproval[0].Command = "secret-command-marker"
	cfg.Events.RequireApproval[0].Args = []string{"secret-arg-marker"}
	cfg.Events.RequireApproval[0].Env = map[string]string{"SAFE_KEY": "secret-env-marker"}
	d := newWithRunner(context.Background(), cfg, logger, runner)

	d.Observe(approvalRequest(map[string]any{"secret": "secret-input-marker"}))
	require.Eventually(t, func() bool { return len(runner.snapshot()) == 1 }, time.Second, time.Millisecond)
	require.NoError(t, d.Close(context.Background()))
	logs := output.String()
	require.Contains(t, logs, "hook command completed")
	require.Contains(t, logs, EventRequireApproval)
	require.Contains(t, logs, "c9cb427bc1387a23c9cb427bc1387a23")
	for _, secret := range []string{"secret-command-marker", "secret-arg-marker", "secret-env-marker", "secret-input-marker"} {
		require.NotContains(t, logs, secret)
	}
}

func TestDispatcherCloseHonorsCallerDeadlineForUncooperativeRunner(t *testing.T) {
	runner := &stubbornRunner{started: make(chan struct{}), release: make(chan struct{})}
	d := newWithRunner(context.Background(), hookConfig(1), nil, runner)
	d.Observe(approvalRequest(nil))
	<-runner.started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, d.Close(ctx), context.DeadlineExceeded)
	close(runner.release)
	require.NoError(t, d.Close(context.Background()))
}

func TestDispatcherConcurrentObserveAndCloseIsRaceSafe(t *testing.T) {
	runner := &captureRunner{}
	cfg := hookConfig(2)
	cfg.Dispatch.MaxConcurrent = 4
	d := newWithRunner(context.Background(), cfg, nil, runner)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 20 {
				d.Observe(approvalRequest(map[string]any{"emitter": i, "call": j}))
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		require.NoError(t, d.Close(context.Background()))
	}()
	wg.Wait()
	require.NoError(t, d.Close(context.Background()))
}

func TestDispatcherCloseRejectsQueuedWorkAndIsIdempotent(t *testing.T) {
	runner := &captureRunner{started: make(chan struct{}), release: make(chan struct{})}
	cfg := hookConfig(1)
	cfg.Dispatch.QueueSize = 2
	d := newWithRunner(context.Background(), cfg, nil, runner)

	d.Observe(approvalRequest(map[string]any{"call": 1}))
	<-runner.started
	d.Observe(approvalRequest(map[string]any{"call": 2}))

	require.NoError(t, d.Close(context.Background()))
	d.Observe(approvalRequest(map[string]any{"call": 3}))
	close(runner.release)
	require.NoError(t, d.Close(context.Background()))
	require.Len(t, runner.snapshot(), 1)
}
