package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
)

var (
	ErrResourceLimit  = errors.New("discovery result exceeds the resource limit")
	ErrResultEncoding = errors.New("discovery result encoding is unavailable")

	pageMaximumTools = int(mustDiscoveryLimit("s2_list_page"))
	pageMaximumBytes = int(mustDiscoveryLimit("tools_list_page_bytes"))
)

type Projector interface {
	Project(context.Context, Request) (Projection, error)
}

type ResultEncoder func(context.Context, ListResult) ([]byte, error)

type ListResult struct {
	Tools      []*Tool `json:"tools"`
	NextCursor string  `json:"nextCursor,omitempty"`
}

type PageRequest struct {
	Lease  *authorization.Lease
	Cursor string
	Encode ResultEncoder
}

type ListPage struct {
	Result  ListResult
	Encoded []byte
}

type Pager struct {
	projector Projector
	cursors   *CursorCodec
}

func NewPager(projector Projector, cursors *CursorCodec) (*Pager, error) {
	if projector == nil || cursors == nil {
		return nil, ErrResultEncoding
	}
	return &Pager{projector: projector, cursors: cursors}, nil
}

func (pager *Pager) List(ctx context.Context, request PageRequest) (ListPage, error) {
	if pager == nil || pager.projector == nil || pager.cursors == nil || request.Encode == nil {
		return ListPage{}, ErrResultEncoding
	}
	if err := ctx.Err(); err != nil {
		return ListPage{}, err
	}
	start := 0
	projectionRequest := Request{Lease: request.Lease}
	if request.Cursor != "" {
		cursor, err := pager.cursors.Decode(request.Cursor, CursorMethodToolsList)
		if err != nil {
			return ListPage{}, err
		}
		start = int(cursor.Position)
		projectionRequest.Continuation = &cursor.Snapshot
	}
	projection, err := pager.projector.Project(ctx, projectionRequest)
	if err != nil {
		return ListPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return ListPage{}, err
	}
	if len(projection.Tools) > int(maximumCursorPosition) {
		return ListPage{}, ErrResultEncoding
	}
	if start < 0 || start > 0 && start >= len(projection.Tools) {
		return ListPage{}, ErrStaleCursor
	}
	if len(projection.Tools) == 0 {
		encoded, err := encodeResult(ctx, request.Encode, ListResult{Tools: make([]*Tool, 0)})
		if err != nil {
			return ListPage{}, err
		}
		if len(encoded) > pageMaximumBytes {
			return ListPage{}, ErrResultEncoding
		}
		return ListPage{Result: ListResult{Tools: make([]*Tool, 0)}, Encoded: encoded}, nil
	}

	remaining := len(projection.Tools) - start
	window := min(pageMaximumTools, remaining)
	lengths := make([]int, 0, window)
	marshalNext := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		tool := projection.Tools[start+len(lengths)]
		if tool == nil {
			return ErrResultEncoding
		}
		encoded, err := json.Marshal(tool)
		if err != nil {
			return fmt.Errorf("%w: encode projected tool: %w", ErrResultEncoding, err)
		}
		lengths = append(lengths, len(encoded))
		return ctx.Err()
	}

	if remaining <= pageMaximumTools {
		for len(lengths) < remaining {
			if err := marshalNext(); err != nil {
				return ListPage{}, err
			}
		}
		baseline, err := encodeResult(ctx, request.Encode, ListResult{Tools: make([]*Tool, 0)})
		if err != nil {
			return ListPage{}, err
		}
		expected := resultSize(len(baseline), lengths, remaining)
		if expected <= pageMaximumBytes {
			return pager.finish(ctx, request.Encode, projection, start, remaining, "", expected)
		}
		window = remaining - 1
	}

	probeCursor, err := pager.cursors.Encode(CursorState{
		Snapshot: projection.Snapshot, Method: CursorMethodToolsList, Position: uint32(start + 1), //nolint:gosec // start is bounded by discoverable_tools.
	})
	if err != nil {
		return ListPage{}, ErrResultEncoding
	}
	baseline, err := encodeResult(ctx, request.Encode, ListResult{Tools: make([]*Tool, 0), NextCursor: probeCursor})
	if err != nil {
		return ListPage{}, err
	}
	if len(baseline) > pageMaximumBytes {
		return ListPage{}, ErrResultEncoding
	}
	accepted := 0
	for accepted < window {
		if len(lengths) == accepted {
			if err := marshalNext(); err != nil {
				return ListPage{}, err
			}
		}
		if resultSize(len(baseline), lengths, accepted+1) > pageMaximumBytes {
			break
		}
		accepted++
	}
	if accepted == 0 {
		return ListPage{}, ErrResourceLimit
	}
	nextPosition := start + accepted
	nextCursor, err := pager.cursors.Encode(CursorState{
		Snapshot: projection.Snapshot, Method: CursorMethodToolsList, Position: uint32(nextPosition), //nolint:gosec // positions are bounded by discoverable_tools.
	})
	if err != nil {
		return ListPage{}, ErrResultEncoding
	}
	expected := resultSize(len(baseline), lengths, accepted)
	return pager.finish(ctx, request.Encode, projection, start, accepted, nextCursor, expected)
}

func (pager *Pager) finish(
	ctx context.Context,
	encode ResultEncoder,
	projection Projection,
	start int,
	count int,
	nextCursor string,
	expectedBytes int,
) (ListPage, error) {
	tools := projection.Tools[start : start+count]
	result := ListResult{Tools: tools, NextCursor: nextCursor}
	encoded, err := encodeResult(ctx, encode, result)
	if err != nil {
		return ListPage{}, err
	}
	if len(encoded) != expectedBytes || len(encoded) > pageMaximumBytes {
		return ListPage{}, ErrResultEncoding
	}
	return ListPage{Result: result, Encoded: encoded}, nil
}

func encodeResult(ctx context.Context, encode ResultEncoder, result ListResult) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encoded, err := encode(ctx, result)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, fmt.Errorf("%w: %w", ErrResultEncoding, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return encoded, nil
}

func resultSize(baseline int, lengths []int, count int) int {
	size := baseline
	for index := range count {
		size += lengths[index]
		if index > 0 {
			size++
		}
	}
	return size
}
