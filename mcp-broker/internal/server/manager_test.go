package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

// mockBackend implements the Backend interface for testing.
type mockBackend struct {
	mock.Mock
}

type blockingCloseBackend struct {
	name    string
	started chan<- string
	release <-chan struct{}
}

func (b *blockingCloseBackend) ListTools(context.Context) ([]Tool, error) {
	return nil, nil
}

func (b *blockingCloseBackend) CallTool(context.Context, string, map[string]any) (*ToolResult, error) {
	return nil, nil
}

func (b *blockingCloseBackend) Close() error {
	b.started <- b.name
	<-b.release
	return nil
}

func (m *mockBackend) ListTools(ctx context.Context) ([]Tool, error) {
	args := m.Called(ctx)
	return args.Get(0).([]Tool), args.Error(1)
}

func (m *mockBackend) CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolResult, error) {
	args := m.Called(ctx, name, arguments)
	return args.Get(0).(*ToolResult), args.Error(1)
}

func (m *mockBackend) Close() error {
	args := m.Called()
	return args.Error(0)
}

func TestManager_CloseClosesBackendsConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	m := &Manager{backends: map[string]Backend{
		"first":  &blockingCloseBackend{name: "first", started: started, release: release},
		"second": &blockingCloseBackend{name: "second", started: started, release: release},
	}}

	done := make(chan error, 1)
	go func() { done <- m.Close() }()

	seen := make(map[string]bool, 2)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for len(seen) < 2 {
		select {
		case name := <-started:
			seen[name] = true
		case <-timer.C:
			close(release)
			<-done
			t.Fatal("backends were closed serially")
		}
	}
	close(release)
	require.NoError(t, <-done)
}

func TestManager_DiscoverTools_PrefixesNames(t *testing.T) {
	mb := new(mockBackend)
	mb.On("ListTools", mock.Anything).Return([]Tool{
		{Name: "search", Description: "Search things"},
		{Name: "get_pr", Description: "Get a PR"},
	}, nil)
	mb.On("Close").Return(nil)

	m := &Manager{
		backends: map[string]Backend{"github": mb},
		tools:    make(map[string]toolEntry),
	}

	err := m.discover(context.Background())
	require.NoError(t, err)

	tools := m.Tools()
	require.Len(t, tools, 2)

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	require.True(t, names["github.search"])
	require.True(t, names["github.get_pr"])
}

func TestManager_DiscoverTools_PreservesAnnotationsOutputSchemaAndMeta(t *testing.T) {
	readOnly := true
	destructive := false
	annotations := &mcp.ToolAnnotation{
		Title:           "Search",
		ReadOnlyHint:    &readOnly,
		DestructiveHint: &destructive,
	}
	outputSchema := &mcp.ToolOutputSchema{
		Type:       "object",
		Properties: map[string]any{"hits": map[string]any{"type": "integer"}},
	}
	meta := &mcp.Meta{AdditionalFields: map[string]any{"trace_id": "abc"}}

	mb := new(mockBackend)
	mb.On("ListTools", mock.Anything).Return([]Tool{
		{
			Name:         "search",
			Description:  "Search things",
			Annotations:  annotations,
			OutputSchema: outputSchema,
			Meta:         meta,
		},
		{Name: "plain", Description: "No extras"},
	}, nil)

	m := &Manager{
		backends: map[string]Backend{"github": mb},
		tools:    make(map[string]toolEntry),
	}
	require.NoError(t, m.discover(context.Background()))

	got := make(map[string]Tool, 2)
	for _, tool := range m.Tools() {
		got[tool.Name] = tool
	}

	rich := got["github.search"]
	require.Equal(t, annotations, rich.Annotations)
	require.Equal(t, outputSchema, rich.OutputSchema)
	require.Equal(t, meta, rich.Meta)

	plain := got["github.plain"]
	require.Nil(t, plain.Annotations)
	require.Nil(t, plain.OutputSchema)
	require.Nil(t, plain.Meta)
}

func TestToBrokerTool_DropsEmptyAnnotationsAndOutputSchema(t *testing.T) {
	bare := toBrokerTool(mcp.Tool{Name: "bare", Description: "no extras"})
	require.Nil(t, bare.Annotations)
	require.Nil(t, bare.OutputSchema)
	require.Nil(t, bare.Meta)

	readOnly := true
	rich := toBrokerTool(mcp.Tool{
		Name:         "rich",
		Annotations:  mcp.ToolAnnotation{ReadOnlyHint: &readOnly},
		OutputSchema: mcp.ToolOutputSchema{Type: "object"},
	})
	require.NotNil(t, rich.Annotations)
	require.True(t, *rich.Annotations.ReadOnlyHint)
	require.NotNil(t, rich.OutputSchema)
	require.Equal(t, "object", rich.OutputSchema.Type)
}

func TestManager_DiscoverTools_AppliesDisabledPatch(t *testing.T) {
	mb := new(mockBackend)
	mb.On("ListTools", mock.Anything).Return([]Tool{
		{Name: "search", Description: "Search"},
		{Name: "delete", Description: "Delete"},
	}, nil)

	m := &Manager{
		backends: map[string]Backend{"github": mb},
		tools:    make(map[string]toolEntry),
		toolPatches: []config.ToolPatchConfig{
			{Tool: "github.delete", Disabled: true},
		},
	}

	require.NoError(t, m.discover(context.Background()))

	tools := m.Tools()
	require.Len(t, tools, 1)
	require.Equal(t, "github.search", tools[0].Name)

	_, err := m.Call(context.Background(), "github.delete", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tool")
}

func TestManager_DiscoverTools_MergesAnnotationPatch(t *testing.T) {
	readOnly := false
	destructive := true
	idempotent := true
	openWorld := false
	backendTitle := "Backend search"
	patchTitle := "Patched search"
	patchedReadOnly := true
	patchedDestructive := false

	mb := new(mockBackend)
	mb.On("ListTools", mock.Anything).Return([]Tool{
		{
			Name:        "search",
			Description: "Search",
			Annotations: &mcp.ToolAnnotation{
				Title:           backendTitle,
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
				OpenWorldHint:   &openWorld,
			},
		},
	}, nil)

	m := &Manager{
		backends: map[string]Backend{"github": mb},
		tools:    make(map[string]toolEntry),
		toolPatches: []config.ToolPatchConfig{
			{
				Tool: "github.search",
				Annotations: &config.ToolAnnotationsPatch{
					Title:           &patchTitle,
					ReadOnlyHint:    &patchedReadOnly,
					DestructiveHint: &patchedDestructive,
				},
			},
		},
	}

	require.NoError(t, m.discover(context.Background()))

	tools := m.Tools()
	require.Len(t, tools, 1)
	annotations := tools[0].Annotations
	require.NotNil(t, annotations)
	require.Equal(t, patchTitle, annotations.Title)
	require.True(t, *annotations.ReadOnlyHint)
	require.False(t, *annotations.DestructiveHint)
	require.True(t, *annotations.IdempotentHint)
	require.False(t, *annotations.OpenWorldHint)
}

func TestManager_DiscoverTools_CreatesAnnotationPatch(t *testing.T) {
	readOnly := true
	mb := new(mockBackend)
	mb.On("ListTools", mock.Anything).Return([]Tool{{Name: "search", Description: "Search"}}, nil)

	m := &Manager{
		backends: map[string]Backend{"github": mb},
		tools:    make(map[string]toolEntry),
		toolPatches: []config.ToolPatchConfig{
			{Tool: "github.search", Annotations: &config.ToolAnnotationsPatch{ReadOnlyHint: &readOnly}},
		},
	}

	require.NoError(t, m.discover(context.Background()))

	tools := m.Tools()
	require.Len(t, tools, 1)
	require.NotNil(t, tools[0].Annotations)
	require.True(t, *tools[0].Annotations.ReadOnlyHint)
}

func TestManager_DiscoverTools_FirstPatchWins(t *testing.T) {
	firstTitle := "first"
	secondTitle := "second"
	mb := new(mockBackend)
	mb.On("ListTools", mock.Anything).Return([]Tool{{Name: "search", Description: "Search"}}, nil)

	m := &Manager{
		backends: map[string]Backend{"github": mb},
		tools:    make(map[string]toolEntry),
		toolPatches: []config.ToolPatchConfig{
			{Tool: "github.*", Annotations: &config.ToolAnnotationsPatch{Title: &firstTitle}},
			{Tool: "github.search", Annotations: &config.ToolAnnotationsPatch{Title: &secondTitle}},
		},
	}

	require.NoError(t, m.discover(context.Background()))

	tools := m.Tools()
	require.Len(t, tools, 1)
	require.NotNil(t, tools[0].Annotations)
	require.Equal(t, firstTitle, tools[0].Annotations.Title)
}

func TestManager_Call_ProxiesToCorrectBackend(t *testing.T) {
	mb := new(mockBackend)
	mb.On("ListTools", mock.Anything).Return([]Tool{
		{Name: "search", Description: "Search"},
	}, nil)
	mb.On("CallTool", mock.Anything, "search", map[string]any{"q": "test"}).
		Return(&ToolResult{Content: "found it"}, nil)

	m := &Manager{
		backends: map[string]Backend{"github": mb},
		tools:    make(map[string]toolEntry),
	}

	err := m.discover(context.Background())
	require.NoError(t, err)

	result, err := m.Call(context.Background(), "github.search", map[string]any{"q": "test"})
	require.NoError(t, err)
	require.Equal(t, "found it", result.Content)

	mb.AssertExpectations(t)
}

func TestManager_Call_UnknownToolReturnsError(t *testing.T) {
	m := &Manager{
		backends: map[string]Backend{},
		tools:    make(map[string]toolEntry),
	}

	_, err := m.Call(context.Background(), "nonexistent.tool", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tool")
}

func TestNewManager_RetriesConnectBeforeDiscovery(t *testing.T) {
	oldConnectBackend := connectBackend
	defer func() { connectBackend = oldConnectBackend }()

	retryCount := 1
	backoffMS := 0
	timeoutSeconds := 0
	attempts := 0
	mb := new(mockBackend)
	mb.On("ListTools", mock.Anything).Return([]Tool{{Name: "search", Description: "Search"}}, nil)
	connectBackend = func(lifetimeCtx context.Context, startupCtx context.Context, name string, srv config.ServerConfig, logger *slog.Logger) (Backend, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("not ready")
		}
		return mb, nil
	}

	m, err := NewManager(context.Background(), map[string]config.ServerConfig{
		"github": {
			StartupRetryCount:     &retryCount,
			StartupRetryBackoffMS: &backoffMS,
			StartupTimeoutSeconds: &timeoutSeconds,
		},
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.Equal(t, 2, attempts)
	require.Len(t, m.Tools(), 1)

	statuses := m.BackendStatuses()
	require.Equal(t, []BackendStatus{{Name: "github", Status: "connected", Attempts: 1, ToolCount: 1}}, statuses)
}

func TestNewManager_ConnectExhaustionIsNonFatalAndRecordsStatus(t *testing.T) {
	oldConnectBackend := connectBackend
	defer func() { connectBackend = oldConnectBackend }()

	retryCount := 1
	backoffMS := 0
	timeoutSeconds := 0
	attempts := 0
	connectBackend = func(lifetimeCtx context.Context, startupCtx context.Context, name string, srv config.ServerConfig, logger *slog.Logger) (Backend, error) {
		attempts++
		return nil, errors.New("temporary unavailable")
	}

	m, err := NewManager(context.Background(), map[string]config.ServerConfig{
		"broken": {
			StartupRetryCount:     &retryCount,
			StartupRetryBackoffMS: &backoffMS,
			StartupTimeoutSeconds: &timeoutSeconds,
		},
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.Empty(t, m.Tools())
	require.Equal(t, 2, attempts)

	statuses := m.BackendStatuses()
	require.Len(t, statuses, 1)
	require.Equal(t, "broken", statuses[0].Name)
	require.Equal(t, "failed", statuses[0].Status)
	require.Equal(t, "connect", statuses[0].Phase)
	require.Equal(t, 2, statuses[0].Attempts)
	require.Equal(t, "backend startup failed; see broker logs", statuses[0].Error)
}

func TestNewManager_MixedHealthyAndFailedBackendsKeepsHealthyTools(t *testing.T) {
	oldConnectBackend := connectBackend
	defer func() { connectBackend = oldConnectBackend }()

	retryCount := 1
	backoffMS := 0
	timeoutSeconds := 0
	healthy := new(mockBackend)
	healthy.On("ListTools", mock.Anything).Return([]Tool{{Name: "search", Description: "Search"}}, nil)
	brokenAttempts := 0
	connectBackend = func(lifetimeCtx context.Context, startupCtx context.Context, name string, srv config.ServerConfig, logger *slog.Logger) (Backend, error) {
		if name == "broken" {
			brokenAttempts++
			return nil, errors.New("temporary unavailable")
		}
		return healthy, nil
	}

	m, err := NewManager(context.Background(), map[string]config.ServerConfig{
		"broken": {
			StartupRetryCount:     &retryCount,
			StartupRetryBackoffMS: &backoffMS,
			StartupTimeoutSeconds: &timeoutSeconds,
		},
		"github": {
			StartupRetryCount:     &retryCount,
			StartupRetryBackoffMS: &backoffMS,
			StartupTimeoutSeconds: &timeoutSeconds,
		},
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.Equal(t, 2, brokenAttempts)
	require.Equal(t, []Tool{{Name: "github.search", Description: "Search"}}, m.Tools())
	require.Equal(t, []BackendStatus{
		{Name: "broken", Status: "failed", Phase: "connect", Attempts: 2, Error: "backend startup failed; see broker logs"},
		{Name: "github", Status: "connected", Attempts: 1, ToolCount: 1},
	}, m.BackendStatuses())
	healthy.AssertExpectations(t)
}

func TestNewManager_ListToolsExhaustionRemovesBackendAndRecordsStatus(t *testing.T) {
	oldConnectBackend := connectBackend
	defer func() { connectBackend = oldConnectBackend }()

	retryCount := 1
	backoffMS := 0
	timeoutSeconds := 0
	mb := new(mockBackend)
	mb.On("ListTools", mock.Anything).Return([]Tool{}, errors.New("tools not ready")).Twice()
	mb.On("Close").Return(nil).Once()
	connectBackend = func(lifetimeCtx context.Context, startupCtx context.Context, name string, srv config.ServerConfig, logger *slog.Logger) (Backend, error) {
		return mb, nil
	}

	m, err := NewManager(context.Background(), map[string]config.ServerConfig{
		"github": {
			StartupRetryCount:     &retryCount,
			StartupRetryBackoffMS: &backoffMS,
			StartupTimeoutSeconds: &timeoutSeconds,
		},
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.Empty(t, m.Tools())

	statuses := m.BackendStatuses()
	require.Len(t, statuses, 1)
	require.Equal(t, "failed", statuses[0].Status)
	require.Equal(t, "list_tools", statuses[0].Phase)
	require.Equal(t, 2, statuses[0].Attempts)
	require.Equal(t, "backend startup failed; see broker logs", statuses[0].Error)
	require.NotContains(t, m.backends, "github")
	mb.AssertExpectations(t)
}

func TestNewManager_OAuthFlowErrorsAreNotRetried(t *testing.T) {
	oldConnectBackend := connectBackend
	defer func() { connectBackend = oldConnectBackend }()

	retryCount := 3
	backoffMS := 0
	timeoutSeconds := 0
	attempts := 0
	connectBackend = func(lifetimeCtx context.Context, startupCtx context.Context, name string, srv config.ServerConfig, logger *slog.Logger) (Backend, error) {
		attempts++
		return nil, errors.New("OAuth flow cancelled: context canceled")
	}

	m, err := NewManager(context.Background(), map[string]config.ServerConfig{
		"github": {
			StartupRetryCount:     &retryCount,
			StartupRetryBackoffMS: &backoffMS,
			StartupTimeoutSeconds: &timeoutSeconds,
		},
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.Equal(t, 1, attempts)
	require.Equal(t, 1, m.BackendStatuses()[0].Attempts)
}

func TestRetryStartup_AuthInteractiveErrorsAreNonRetryable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "oauth flow", err: errors.New("OAuth flow cancelled: context canceled")},
		{name: "authorization denied", err: errors.New("authorization denied by user")},
		{name: "authorization required", err: errors.New("authorization required")},
		{name: "oauth callback", err: errors.New("OAuth callback error: access_denied")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			retryCount := 3
			backoffMS := 0
			attemptsSeen := 0
			attempts, err := retryStartup(context.Background(), config.ServerConfig{
				StartupRetryCount:     &retryCount,
				StartupRetryBackoffMS: &backoffMS,
			}, func(_ context.Context, _ context.Context) error {
				attemptsSeen++
				return tc.err
			})
			require.ErrorIs(t, err, tc.err)
			require.Equal(t, 1, attempts)
			require.Equal(t, 1, attemptsSeen)
		})
	}
}

func TestRetryStartup_UsesStartupTimeoutInsteadOfHTTPTimeout(t *testing.T) {
	retryCount := 0
	timeoutSeconds := 1
	attempts, err := retryStartup(context.Background(), config.ServerConfig{
		HTTPTimeoutSeconds:    120,
		StartupRetryCount:     &retryCount,
		StartupTimeoutSeconds: &timeoutSeconds,
	}, func(_ context.Context, ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.WithinDuration(t, time.Now().Add(time.Second), deadline, 250*time.Millisecond)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, attempts)
}

func TestRetryStartup_StopsWhenParentContextIsCanceled(t *testing.T) {
	retryCount := 3
	backoffMS := 0
	timeoutSeconds := 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	attempts, err := retryStartup(ctx, config.ServerConfig{
		StartupRetryCount:     &retryCount,
		StartupRetryBackoffMS: &backoffMS,
		StartupTimeoutSeconds: &timeoutSeconds,
	}, func(_ context.Context, ctx context.Context) error {
		return ctx.Err()
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, attempts)
}

func TestRetryStartup_DefaultRetryCountAndExplicitZero(t *testing.T) {
	backoffMS := 0
	defaultAttempts := 0
	attempts, err := retryStartup(context.Background(), config.ServerConfig{StartupRetryBackoffMS: &backoffMS}, func(_ context.Context, _ context.Context) error {
		defaultAttempts++
		if defaultAttempts < 4 {
			return errors.New("not ready")
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 4, attempts)
	require.Equal(t, 4, defaultAttempts)

	retryCount := 0
	zeroAttempts := 0
	attempts, err = retryStartup(context.Background(), config.ServerConfig{StartupRetryCount: &retryCount}, func(_ context.Context, _ context.Context) error {
		zeroAttempts++
		return errors.New("not ready")
	})
	require.Error(t, err)
	require.Equal(t, 1, attempts)
	require.Equal(t, 1, zeroAttempts)
}

func TestRetryStartup_BackoffStopsOnContextCancellation(t *testing.T) {
	retryCount := 3
	backoffMS := 10_000
	timeoutSeconds := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstAttemptDone := make(chan struct{})

	go func() {
		<-firstAttemptDone
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	attempts, err := retryStartup(ctx, config.ServerConfig{
		StartupRetryCount:     &retryCount,
		StartupRetryBackoffMS: &backoffMS,
		StartupTimeoutSeconds: &timeoutSeconds,
	}, func(_ context.Context, _ context.Context) error {
		close(firstAttemptDone)
		return errors.New("not ready")
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, attempts)
	require.Less(t, time.Since(started), 500*time.Millisecond)
}

func TestRetryStartup_SeparatesLifetimeContextFromStartupTimeout(t *testing.T) {
	retryCount := 0
	timeoutSeconds := 1
	attempts, err := retryStartup(context.Background(), config.ServerConfig{
		StartupRetryCount:     &retryCount,
		StartupTimeoutSeconds: &timeoutSeconds,
	}, func(lifetimeCtx context.Context, startupCtx context.Context) error {
		_, lifetimeHasDeadline := lifetimeCtx.Deadline()
		startupDeadline, startupHasDeadline := startupCtx.Deadline()
		require.False(t, lifetimeHasDeadline)
		require.True(t, startupHasDeadline)
		require.WithinDuration(t, time.Now().Add(time.Second), startupDeadline, 250*time.Millisecond)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, attempts)
}

func TestSummarizeStartupErrorAvoidsRawUnexpectedErrorText(t *testing.T) {
	require.Equal(t, "connection refused", summarizeStartupError(errors.New("dial tcp 127.0.0.1:9: connection refused")))
	require.Equal(t, "backend startup failed; see broker logs", summarizeStartupError(errors.New("GET https://secret.example/mcp?token=abc failed")))
}

func TestConnect_UnknownTypeDefaultsToStdio(t *testing.T) {
	// connect() with empty type should attempt stdio (which will fail without a real command,
	// but the error message confirms the routing)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := connect(ctx, "test", config.ServerConfig{Type: "", Command: "/nonexistent"}, logger)
	require.Error(t, err)
	require.Contains(t, err.Error(), "spawn stdio server")
}

func TestConnect_StreamableHTTPType(t *testing.T) {
	// connect() with "streamable-http" should attempt HTTP (which will fail without a real server,
	// but the error confirms routing)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := connect(ctx, "test", config.ServerConfig{Type: "streamable-http", URL: "http://localhost:1/nonexistent"}, logger)
	require.Error(t, err)
	require.Contains(t, err.Error(), "initialize server")
}

func TestConnect_StreamableHTTPFailsGracefully(t *testing.T) {
	// When connecting to a non-existent HTTP server, connect should fail
	// with an error that includes the server name for debugging.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, err := connect(ctx, "broken", config.ServerConfig{
		Type: "streamable-http",
		URL:  "http://127.0.0.1:1/nonexistent",
	}, logger)
	require.Error(t, err)
	require.Contains(t, err.Error(), "broken")
}

func TestExpandEnv_SubstitutesVariables(t *testing.T) {
	t.Setenv("MY_TOKEN", "secret123")
	env := map[string]string{
		"TOKEN":  "$MY_TOKEN",
		"STATIC": "plainvalue",
	}
	result := expandEnv(env)
	require.Equal(t, "secret123", result["TOKEN"])
	require.Equal(t, "plainvalue", result["STATIC"])
}

func TestExpandEnv_EmbeddedVariables(t *testing.T) {
	t.Setenv("MY_TOKEN", "ghp_abc123")
	env := map[string]string{
		"AUTH": "Bearer $MY_TOKEN",
	}
	result := expandEnv(env)
	require.Equal(t, "Bearer ghp_abc123", result["AUTH"])
}

func TestExpandEnv_BraceSyntax(t *testing.T) {
	t.Setenv("MY_TOKEN", "secret123")
	env := map[string]string{
		"TOKEN": "${MY_TOKEN}",
	}
	result := expandEnv(env)
	require.Equal(t, "secret123", result["TOKEN"])
}
