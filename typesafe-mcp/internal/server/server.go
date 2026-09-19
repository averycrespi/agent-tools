// Package server composes TypeSafe's strict MCP tools and bounded stdio transport.
package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"

	"github.com/averycrespi/agent-tools/typesafe-mcp/internal/provider"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

const MaxFrameBytes = 1 << 20

func New(client *provider.Client) *mcpserver.MCPServer {
	srv := mcpserver.NewMCPServer("typesafe-mcp", "0.1.0", mcpserver.WithStrictInputSchemaDefault(), mcpserver.WithInputSchemaValidation())
	evaluate := mcp.NewToolWithRawSchema("evaluate", "Evaluate agent-defined questions with TypeSafe. Discloses inputs and consumes paid quota; confidence is not authorization.", provider.EvaluateSchema)
	evaluate.RawOutputSchema = provider.EvaluateOutputSchema
	evaluate.Annotations = mcp.ToolAnnotation{ReadOnlyHint: ptr(false), DestructiveHint: ptr(false), IdempotentHint: ptr(false), OpenWorldHint: ptr(true)}
	models := mcp.NewToolWithRawSchema("list_models", "List TypeSafe model names, descriptions and release dates. Listing is not an allowlist.", provider.ModelsSchema)
	models.RawOutputSchema = provider.ModelsOutputSchema
	models.Annotations = mcp.ToolAnnotation{ReadOnlyHint: ptr(true), DestructiveHint: ptr(false), IdempotentHint: ptr(true), OpenWorldHint: ptr(true)}
	for _, tool := range []mcp.Tool{evaluate, models} {
		srv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			if args == nil {
				args = map[string]any{}
			}
			result, err := client.Call(ctx, req.Params.Name, args)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			// Fixed fallback avoids a second escaped copy of the provider response.
			return mcp.NewToolResultStructured(result, "TypeSafe result is available in structuredContent."), nil
		})
	}
	return srv
}
func ptr(value bool) *bool { return &value }

func Serve(ctx context.Context, client *provider.Client, input io.Reader, output io.Writer) error {
	stdio := mcpserver.NewStdioServer(New(client))
	mcpserver.WithErrorLogger(log.New(io.Discard, "", 0))(stdio)
	mcpserver.WithWorkerPoolSize(provider.MaxConcurrent)(stdio)
	mcpserver.WithQueueSize(provider.MaxConcurrent)(stdio)
	err := stdio.Listen(ctx, &frames{reader: bufio.NewReader(input)}, output)
	if err != nil {
		return errors.New("stdio: invalid, oversized, or interrupted protocol stream")
	}
	return nil
}

// Limit each complete frame before mcp-go allocates/decodes or queues it.
type frames struct {
	reader  *bufio.Reader
	pending []byte
}

func (f *frames) Read(destination []byte) (int, error) {
	if len(f.pending) == 0 {
		var frame []byte
		for {
			piece, err := f.reader.ReadSlice('\n')
			if len(frame)+len(piece) > MaxFrameBytes+1 {
				return 0, errors.New("frame limit")
			}
			frame = append(frame, piece...)
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if err != nil {
				if errors.Is(err, io.EOF) && len(frame) == 0 {
					return 0, io.EOF
				}
				return 0, errors.New("incomplete frame")
			}
			break
		}
		if !provider.BoundedJSON(frame, provider.MaxDepth+8) {
			return 0, errors.New("invalid frame")
		}
		f.pending = frame
	}
	count := copy(destination, f.pending)
	f.pending = f.pending[count:]
	return count, nil
}
