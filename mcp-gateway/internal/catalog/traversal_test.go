package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTraversalGoldenModernAndLegacyIsolatesMalformedDescriptors(t *testing.T) {
	for _, era := range []string{"modern", "legacy"} {
		t.Run(era, func(t *testing.T) {
			client := &scriptedClient{results: []json.RawMessage{
				json.RawMessage(`{"tools":[{"name":"alpha","inputSchema":{"type":"object"}},null,{"name":"bad","name":"duplicate"}],"nextCursor":"cursor-1","_meta":{"ignored":true}}`),
				json.RawMessage(`{"tools":[{"name":"beta","inputSchema":{"type":"object"}}],"nextCursor":null}`),
			}}
			candidate, err := NewTraverser().Traverse(context.Background(), client, "sample")
			require.NoError(t, err)
			assert.Equal(t, int64(2), candidate.Pages)
			assert.Equal(t, int64(4), candidate.RawCount)
			assert.Len(t, candidate.Issues, 2)
			assert.Equal(t, []string{"alpha", "beta"}, []string{candidate.Tools[0].UpstreamName, candidate.Tools[1].UpstreamName})
			assert.Equal(t, []string{"sample.alpha", "sample.beta"}, []string{candidate.Tools[0].ExternalName, candidate.Tools[1].ExternalName})
			assert.JSONEq(t, `{"cursor":""}`, string(client.params[0]))
			assert.JSONEq(t, `{"cursor":"cursor-1"}`, string(client.params[1]))
		})
	}
}

func TestTraversalProjectsOnlyFirstPageOAuthChallenge(t *testing.T) {
	disposition := &downstream.OAuthChallengeDisposition{Kind: downstream.OAuthChallengeRefresh, Stage: downstream.OAuthChallengeCatalogFirstPage, Metadata: []string{"https://resource.example/metadata"}}
	first := &challengePageClient{challenge: disposition}
	_, err := NewTraverser().Traverse(context.Background(), first, "sample")
	var projected *downstream.OAuthChallengeDisposition
	require.ErrorAs(t, err, &projected)
	assert.Same(t, disposition, projected)
	assert.Equal(t, 1, first.calls)

	later := &challengePageClient{first: json.RawMessage(`{"tools":[],"nextCursor":"next"}`), challenge: disposition}
	_, err = NewTraverser().Traverse(context.Background(), later, "sample")
	assert.ErrorIs(t, err, downstream.ErrAuthenticationRejected)
	assert.False(t, errors.As(err, &projected))
	assert.Equal(t, 2, later.calls)
}

func TestTraversalRejectsMalformedPagesCursorsCyclesAndCollisions(t *testing.T) {
	tests := []struct {
		name    string
		results []json.RawMessage
		err     error
	}{
		{name: "missing tools", results: []json.RawMessage{json.RawMessage(`{"nextCursor":null}`)}, err: ErrInvalidPage},
		{name: "null tools", results: []json.RawMessage{json.RawMessage(`{"tools":null}`)}, err: ErrInvalidPage},
		{name: "invalid cursor type", results: []json.RawMessage{json.RawMessage(`{"tools":[],"nextCursor":1}`)}, err: ErrInvalidPage},
		{name: "duplicate page member", results: []json.RawMessage{json.RawMessage(`{"tools":[],"tools":[]}`)}, err: ErrInvalidPage},
		{name: "cursor cycle", results: []json.RawMessage{json.RawMessage(`{"tools":[],"nextCursor":"same"}`), json.RawMessage(`{"tools":[],"nextCursor":"same"}`)}, err: ErrCursorCycle},
		{name: "duplicate name", results: []json.RawMessage{json.RawMessage(`{"tools":[{"name":"same"},{"name":"same"}]}`)}, err: ErrNameCollision},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewTraverser().Traverse(context.Background(), &scriptedClient{results: test.results}, "sample")
			assert.ErrorIs(t, err, test.err)
		})
	}
}

func TestTraversalEnforcesPageToolDescriptorAndNameBounds(t *testing.T) {
	validTools := make([]string, fixedLimit("active_tools_per_server"))
	for index := range validTools {
		validTools[index] = fmt.Sprintf(`{"name":"tool_%d"}`, index)
	}
	atLimit := json.RawMessage(`{"tools":[` + strings.Join(validTools, ",") + `]}`)
	candidate, err := NewTraverser().Traverse(context.Background(), &scriptedClient{results: []json.RawMessage{atLimit}}, "sample")
	require.NoError(t, err)
	assert.Len(t, candidate.Tools, int(fixedLimit("active_tools_per_server")))
	overLimit := json.RawMessage(`{"tools":[` + strings.Join(append(validTools, `{"name":"overflow"}`), ",") + `]}`)
	_, err = NewTraverser().Traverse(context.Background(), &scriptedClient{results: []json.RawMessage{overLimit}}, "sample")
	assert.ErrorIs(t, err, ErrTraversalLimit)

	descriptor := sizedPage(t, int(fixedLimit("tool_descriptor_bytes")))
	candidate, err = NewTraverser().Traverse(context.Background(), &scriptedClient{results: []json.RawMessage{json.RawMessage(`{"tools":[` + descriptor + `]}`)}}, "sample")
	require.NoError(t, err)
	assert.Len(t, candidate.Tools, 1)
	overDescriptor := sizedPage(t, int(fixedLimit("tool_descriptor_bytes"))+1)
	candidate, err = NewTraverser().Traverse(context.Background(), &scriptedClient{results: []json.RawMessage{json.RawMessage(`{"tools":[` + overDescriptor + `]}`)}}, "sample")
	require.NoError(t, err)
	assert.Empty(t, candidate.Tools)
	assert.Len(t, candidate.Issues, 1)

	page := paddedPage(t, int(fixedLimit("tools_list_page_bytes")))
	_, err = NewTraverser().Traverse(context.Background(), &scriptedClient{results: []json.RawMessage{page}}, "sample")
	require.NoError(t, err)
	_, err = NewTraverser().Traverse(context.Background(), &scriptedClient{results: []json.RawMessage{append(page, ' ')}}, "sample")
	assert.ErrorIs(t, err, ErrInvalidPage)

	invalidNames := []string{"", "space name", "slash/name", strings.Repeat("a", int(fixedLimit("tool_name_bytes"))+1)}
	for _, name := range invalidNames {
		body, _ := json.Marshal(map[string]any{"tools": []any{map[string]any{"name": name}}})
		candidate, err := NewTraverser().Traverse(context.Background(), &scriptedClient{results: []json.RawMessage{body}}, "sample")
		require.NoError(t, err)
		assert.Empty(t, candidate.Tools)
		assert.Len(t, candidate.Issues, 1)
	}
}

func TestTraversalRejectsPage32Continuation(t *testing.T) {
	results := make([]json.RawMessage, fixedLimit("tools_list_pages"))
	for index := range results {
		results[index] = json.RawMessage(fmt.Sprintf(`{"tools":[],"nextCursor":"cursor-%d"}`, index+1))
	}
	_, err := NewTraverser().Traverse(context.Background(), &scriptedClient{results: results}, "sample")
	assert.ErrorIs(t, err, ErrTraversalLimit)
}

func TestTraversalUsesExactDeadlinesAndCancellation(t *testing.T) {
	var durations []time.Duration
	deadline := func(ctx context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
		durations = append(durations, duration)
		return context.WithCancel(ctx)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewTraverserWithDeadline(deadline).Traverse(ctx, &scriptedClient{waitForCancellation: true}, "sample")
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.Equal(t, []time.Duration{contract.CatalogTraversalDeadline, contract.CatalogPageDeadline}, durations)
}

func TestTraversalAdmissionRejectsFifthWithoutQueue(t *testing.T) {
	traverser := NewTraverser()
	client := &blockingClient{started: make(chan struct{}, 4), release: make(chan struct{})}
	results := make(chan error, 4)
	for range 4 {
		go func() {
			_, err := traverser.Traverse(context.Background(), client, "sample")
			results <- err
		}()
	}
	for range 4 {
		<-client.started
	}
	assert.Equal(t, contract.LimitStatus{InUse: 4, Limit: 4, Saturated: true}, traverser.Status())
	_, err := traverser.Traverse(context.Background(), client, "sample")
	assert.ErrorIs(t, err, ErrTraversalLimit)
	close(client.release)
	for range 4 {
		require.NoError(t, <-results)
	}
}

type scriptedClient struct {
	mu                  sync.Mutex
	results             []json.RawMessage
	params              []json.RawMessage
	waitForCancellation bool
}

func (client *scriptedClient) Request(ctx context.Context, method string, params json.RawMessage, name string) (downstream.Response, error) {
	if method != "tools/list" || name != "" {
		return downstream.Response{}, errors.New("unexpected request")
	}
	if client.waitForCancellation {
		<-ctx.Done()
		return downstream.Response{}, ctx.Err()
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	client.params = append(client.params, append(json.RawMessage(nil), params...))
	if len(client.results) == 0 {
		return downstream.Response{}, errors.New("no response")
	}
	result := client.results[0]
	client.results = client.results[1:]
	return downstream.Response{Result: append(json.RawMessage(nil), result...)}, nil
}

type challengePageClient struct {
	first     json.RawMessage
	challenge *downstream.OAuthChallengeDisposition
	calls     int
}

func (client *challengePageClient) Request(context.Context, string, json.RawMessage, string) (downstream.Response, error) {
	client.calls++
	if client.calls == 1 && client.first != nil {
		return downstream.Response{Result: client.first}, nil
	}
	return downstream.Response{}, client.challenge
}

type blockingClient struct {
	started chan struct{}
	release chan struct{}
}

func (client *blockingClient) Request(context.Context, string, json.RawMessage, string) (downstream.Response, error) {
	client.started <- struct{}{}
	<-client.release
	return downstream.Response{Result: json.RawMessage(`{"tools":[]}`)}, nil
}

func sizedPage(t *testing.T, size int) string {
	t.Helper()
	prefix, suffix := `{"name":"tool","padding":"`, `"}`
	require.GreaterOrEqual(t, size, len(prefix)+len(suffix))
	return prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
}

func paddedPage(t *testing.T, size int) json.RawMessage {
	t.Helper()
	prefix, suffix := `{"tools":[],"padding":"`, `"}`
	require.GreaterOrEqual(t, size, len(prefix)+len(suffix))
	return json.RawMessage(prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix)
}
