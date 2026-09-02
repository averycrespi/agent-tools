package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeProjector struct {
	projection Projection
	err        error
	calls      int
	requests   []Request
	after      func()
}

func (projector *fakeProjector) Project(_ context.Context, request Request) (Projection, error) {
	projector.calls++
	projector.requests = append(projector.requests, request)
	if projector.after != nil {
		projector.after()
	}
	return projector.projection, projector.err
}

type countingEncoder struct {
	calls          int
	maxTools       int
	baselineBytes  int
	cursorOverhead int
	bytesPerTool   int
	mutate         bool
	id             json.RawMessage
}

func (encoder *countingEncoder) encode(_ context.Context, result ListResult) ([]byte, error) {
	encoder.calls++
	encoder.maxTools = max(encoder.maxTools, len(result.Tools))
	if encoder.bytesPerTool > 0 {
		size := encoder.baselineBytes + len(result.Tools)*encoder.bytesPerTool
		if len(result.Tools) > 1 {
			size += len(result.Tools) - 1
		}
		if result.NextCursor != "" {
			size += encoder.cursorOverhead + len(result.NextCursor)
		}
		return make([]byte, size), nil
	}
	id := encoder.id
	if len(id) == 0 {
		id = json.RawMessage(`17`)
	}
	encoded, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  ListResult      `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: result})
	if err == nil && encoder.mutate && len(result.Tools) > 0 {
		encoded = append(encoded, byte('\n'))
	}
	return encoded, err
}

func TestPagerEmitsEmptyAndCountBoundedPagesWithRealCursors(t *testing.T) {
	codec := mustCursorCodec(t, 11)
	emptyProjector := &fakeProjector{projection: Projection{Tools: []*Tool{}, Snapshot: testCursorState().Snapshot}}
	emptyEncoder := &countingEncoder{}
	emptyPager := mustPager(t, emptyProjector, codec)
	empty, err := emptyPager.List(t.Context(), PageRequest{Encode: emptyEncoder.encode})
	require.NoError(t, err)
	assert.Empty(t, empty.Result.Tools)
	assert.Empty(t, empty.Result.NextCursor)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":17,"result":{"tools":[]}}`, string(empty.Encoded))

	tools := compactTools(101)
	projector := &fakeProjector{projection: Projection{Tools: tools, Snapshot: testCursorState().Snapshot}}
	encoder := &countingEncoder{}
	pager := mustPager(t, projector, codec)
	first, err := pager.List(t.Context(), PageRequest{Encode: encoder.encode})
	require.NoError(t, err)
	assert.Len(t, first.Result.Tools, 100)
	require.NotEmpty(t, first.Result.NextCursor)
	state, err := codec.Decode(first.Result.NextCursor, CursorMethodToolsList)
	require.NoError(t, err)
	assert.Equal(t, uint32(100), state.Position)
	assert.LessOrEqual(t, len(first.Encoded), pageMaximumBytes)

	second, err := pager.List(t.Context(), PageRequest{Cursor: first.Result.NextCursor, Encode: encoder.encode})
	require.NoError(t, err)
	assert.Len(t, second.Result.Tools, 1)
	assert.Equal(t, "tool-0100", second.Result.Tools[0].Name)
	assert.Empty(t, second.Result.NextCursor)
	require.Len(t, projector.requests, 2)
	require.NotNil(t, projector.requests[1].Continuation)
	assert.Equal(t, state.Snapshot, *projector.requests[1].Continuation)
	assert.LessOrEqual(t, encoder.maxTools, 100)
}

func TestPagerIsMaximalAtExactEncodedBoundaryAndHandlesFinalCursorOmission(t *testing.T) {
	codec := mustCursorCodec(t, 12)
	encoder := &countingEncoder{}

	exact := minimalTool("exact")
	exact.Description = "x"
	oneByte, err := encoder.encode(t.Context(), ListResult{Tools: []*Tool{exact}})
	require.NoError(t, err)
	exact.Description = strings.Repeat("x", pageMaximumBytes-(len(oneByte)-1))
	projector := &fakeProjector{projection: Projection{Tools: []*Tool{exact}, Snapshot: testCursorState().Snapshot}}
	page, err := mustPager(t, projector, codec).List(t.Context(), PageRequest{Encode: encoder.encode})
	require.NoError(t, err)
	assert.Len(t, page.Encoded, pageMaximumBytes)
	assert.Empty(t, page.Result.NextCursor)

	exact.Description += "x"
	_, err = mustPager(t, projector, codec).List(t.Context(), PageRequest{Encode: encoder.encode})
	assert.ErrorIs(t, err, ErrResourceLimit)

	small := minimalTool("small")
	first := minimalTool("first")
	first.Description = "x"
	probe, err := encoder.encode(t.Context(), ListResult{Tools: []*Tool{first, small}})
	require.NoError(t, err)
	first.Description = strings.Repeat("x", pageMaximumBytes-(len(probe)-1))
	state := testCursorState()
	oneCursor, err := codec.Encode(CursorState{Snapshot: state.Snapshot, Method: CursorMethodToolsList, Position: 1})
	require.NoError(t, err)
	oneNonFinal, err := encoder.encode(t.Context(), ListResult{Tools: []*Tool{first}, NextCursor: oneCursor})
	require.NoError(t, err)
	assert.Greater(t, len(oneNonFinal), pageMaximumBytes)
	projector = &fakeProjector{projection: Projection{Tools: []*Tool{first, small}, Snapshot: state.Snapshot}}
	page, err = mustPager(t, projector, codec).List(t.Context(), PageRequest{Encode: encoder.encode})
	require.NoError(t, err)
	assert.Len(t, page.Result.Tools, 2)
	assert.Empty(t, page.Result.NextCursor)
	assert.Len(t, page.Encoded, pageMaximumBytes)
}

func TestPagerNearBoundaryIsExactlyMaximalAndPreservesFields(t *testing.T) {
	codec := mustCursorCodec(t, 13)
	tools := compactTools(40)
	for _, tool := range tools {
		tool.Title = "Preserved title"
		tool.Description = strings.Repeat(`x"<&`, 25_000)
		tool.InputSchema = json.RawMessage(`{"type":"object","properties":{"quoted":{"const":"\\\"<&"}}}`)
		tool.OutputSchema = json.RawMessage(`{"type":"string"}`)
		tool.Annotations.Title = "Preserved annotation"
	}
	projector := &fakeProjector{projection: Projection{Tools: tools, Snapshot: testCursorState().Snapshot}}
	encoder := &countingEncoder{}
	pager := mustPager(t, projector, codec)
	page, err := pager.List(t.Context(), PageRequest{Encode: encoder.encode})
	require.NoError(t, err)
	assert.NotEmpty(t, page.Result.NextCursor)
	assert.LessOrEqual(t, len(page.Encoded), pageMaximumBytes)
	state, err := codec.Decode(page.Result.NextCursor, CursorMethodToolsList)
	require.NoError(t, err)
	assert.Equal(t, uint32(len(page.Result.Tools)), state.Position)

	next := tools[len(page.Result.Tools)]
	candidateCursor, err := codec.Encode(CursorState{Snapshot: state.Snapshot, Method: CursorMethodToolsList, Position: state.Position + 1})
	require.NoError(t, err)
	candidate := ListResult{Tools: append(append([]*Tool(nil), page.Result.Tools...), next), NextCursor: candidateCursor}
	candidateBytes, err := encoder.encode(t.Context(), candidate)
	require.NoError(t, err)
	assert.Greater(t, len(candidateBytes), pageMaximumBytes)
	assert.Contains(t, string(page.Encoded), `"title":"Preserved title"`)
	assert.Contains(t, string(page.Encoded), `"inputSchema":{"type":"object"`)
	assert.Contains(t, string(page.Encoded), `"outputSchema":{"type":"string"}`)
	assert.Contains(t, string(page.Encoded), `"title":"Preserved annotation"`)
	assert.Contains(t, string(page.Encoded), `"annotations":`)
}

func TestPagerReachesAll2048ToolsAndMoreThan32LargePages(t *testing.T) {
	codec := mustCursorCodec(t, 14)
	byteLimited := byteLimitedTools(2048)
	encodedTool, err := json.Marshal(byteLimited[0])
	require.NoError(t, err)
	realEncoder := &countingEncoder{}
	empty, err := realEncoder.encode(t.Context(), ListResult{Tools: []*Tool{}})
	require.NoError(t, err)
	withCursor, err := realEncoder.encode(t.Context(), ListResult{Tools: []*Tool{}, NextCursor: "cursor"})
	require.NoError(t, err)
	cursorOverhead := len(withCursor) - len(empty) - len("cursor")
	for _, test := range []struct {
		name         string
		tools        []*Tool
		minimumPages int
		bytesPerTool int
	}{
		{name: "compact", tools: compactTools(2048), minimumPages: 21},
		{name: "byte limited", tools: byteLimited, minimumPages: 33, bytesPerTool: len(encodedTool)},
	} {
		t.Run(test.name, func(t *testing.T) {
			projector := &fakeProjector{projection: Projection{Tools: test.tools, Snapshot: testCursorState().Snapshot}}
			encoder := &countingEncoder{
				baselineBytes: len(empty), cursorOverhead: cursorOverhead, bytesPerTool: test.bytesPerTool,
			}
			pager := mustPager(t, projector, codec)
			cursor := ""
			seen := make([]string, 0, len(test.tools))
			pages := 0
			for {
				page, err := pager.List(t.Context(), PageRequest{Cursor: cursor, Encode: encoder.encode})
				require.NoError(t, err)
				pages++
				assert.NotEmpty(t, page.Result.Tools)
				assert.LessOrEqual(t, len(page.Result.Tools), 100)
				assert.LessOrEqual(t, len(page.Encoded), pageMaximumBytes)
				for _, tool := range page.Result.Tools {
					seen = append(seen, tool.Name)
				}
				cursor = page.Result.NextCursor
				if cursor == "" {
					break
				}
			}
			assert.GreaterOrEqual(t, pages, test.minimumPages)
			assert.Equal(t, toolNames(test.tools), seen)
			assert.LessOrEqual(t, encoder.maxTools, 100)
			assert.LessOrEqual(t, encoder.calls, pages*3)
		})
	}
}

func TestPagerRejectsStalePositionsCancellationAndEncodingFaults(t *testing.T) {
	codec := mustCursorCodec(t, 15)
	state := testCursorState()
	projector := &fakeProjector{projection: Projection{Tools: compactTools(2), Snapshot: state.Snapshot}}
	pager := mustPager(t, projector, codec)
	for _, position := range []uint32{2, 3, 2048} {
		cursor, err := codec.Encode(CursorState{Snapshot: state.Snapshot, Method: CursorMethodToolsList, Position: position})
		require.NoError(t, err)
		_, err = pager.List(t.Context(), PageRequest{Cursor: cursor, Encode: (&countingEncoder{}).encode})
		assert.ErrorIs(t, err, ErrStaleCursor)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := pager.List(ctx, PageRequest{Encode: (&countingEncoder{}).encode})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 3, projector.calls)

	ctx, cancel = context.WithCancel(t.Context())
	projector.after = cancel
	_, err = pager.List(ctx, PageRequest{Encode: (&countingEncoder{}).encode})
	assert.ErrorIs(t, err, context.Canceled)
	projector.after = nil

	projector.err = authorization.ErrAuthorizationUnavailable
	_, err = pager.List(t.Context(), PageRequest{Encode: (&countingEncoder{}).encode})
	assert.ErrorIs(t, err, authorization.ErrAuthorizationUnavailable)
	projector.err = nil

	_, err = pager.List(t.Context(), PageRequest{})
	assert.ErrorIs(t, err, ErrResultEncoding)
	mutating := &countingEncoder{mutate: true}
	_, err = pager.List(t.Context(), PageRequest{Encode: mutating.encode})
	assert.ErrorIs(t, err, ErrResultEncoding)
	projector.projection.Tools = []*Tool{{Name: "invalid", InputSchema: json.RawMessage(`{`)}}
	_, err = pager.List(t.Context(), PageRequest{Encode: (&countingEncoder{}).encode})
	assert.ErrorIs(t, err, ErrResultEncoding)
	assert.NotErrorIs(t, err, ErrResourceLimit)
}

func compactTools(count int) []*Tool {
	tools := make([]*Tool, count)
	for index := range count {
		tools[index] = minimalTool(fmt.Sprintf("tool-%04d", index))
	}
	return tools
}

func byteLimitedTools(count int) []*Tool {
	schema := json.RawMessage(`{"type":"object","description":"` + strings.Repeat("s", 64*1024) + `"}`)
	tools := make([]*Tool, count)
	for index := range count {
		tools[index] = &Tool{
			Name: fmt.Sprintf("large-%04d", index), InputSchema: schema,
			Annotations: &ToolAnnotations{DestructiveHint: boolPointer(true), OpenWorldHint: boolPointer(true)},
		}
	}
	return tools
}

func minimalTool(name string) *Tool {
	return &Tool{
		Name: name, InputSchema: json.RawMessage(`{"type":"object"}`),
		Annotations: &ToolAnnotations{DestructiveHint: boolPointer(true), OpenWorldHint: boolPointer(true)},
	}
}

func mustPager(t *testing.T, projector Projector, codec *CursorCodec) *Pager {
	t.Helper()
	pager, err := NewPager(projector, codec)
	require.NoError(t, err)
	return pager
}
