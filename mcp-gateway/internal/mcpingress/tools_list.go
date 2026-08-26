package mcpingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

var (
	errInvalidToolsListParams            = errors.New("tools/list parameters are invalid")
	ErrToolsListInvalidParams            = errors.New("tools/list parameters have invalid syntax")
	ErrToolsListStaleCursor              = errors.New("tools/list cursor is stale")
	ErrToolsListAuthorizationUnavailable = errors.New("tools/list authorization is unavailable")
	ErrToolsListResourceLimit            = errors.New("tools/list resource limit is reached")
)

type ToolsListEncoder func(context.Context, any, string) ([]byte, error)

type ToolsListService interface {
	ListTools(context.Context, *authorization.Lease, string, ToolsListEncoder) ([]byte, error)
}

type toolsListCall struct {
	ID     json.RawMessage
	Cursor string
}

type toolsListParams struct {
	Cursor json.RawMessage `json:"cursor"`
	Meta   json.RawMessage `json:"_meta"`
}

type rpcErrorKind uint8

const (
	rpcInvalidParams rpcErrorKind = iota + 1
	rpcStaleCursor
	rpcAuthorizationUnavailable
	rpcResourceLimit
	rpcMethodNotFound
)

type toolsListResult struct {
	Tools      any    `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

type rpcSuccessEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  toolsListResult `json:"result"`
}

type rpcErrorEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   rpcError        `json:"error"`
}

type rpcError struct {
	Code    int           `json:"code"`
	Message string        `json:"message"`
	Data    *rpcErrorData `json:"data,omitempty"`
}

type rpcErrorData struct {
	Code string `json:"code"`
}

func parseToolsList(wire wireRequest) (toolsListCall, error) {
	call := toolsListCall{ID: copyRequestID(wire.ID)}
	if len(wire.Params) == 0 {
		return call, nil
	}
	trimmed := bytes.TrimSpace(wire.Params)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return toolsListCall{}, errInvalidToolsListParams
	}
	var params toolsListParams
	if err := strictjson.Decode(trimmed, &params, strictjson.Options{
		MaxBytes: int64(limit("mcp_body_bytes")), MaxDepth: limit("json_depth"), RejectUnknownMembers: true,
	}); err != nil {
		return toolsListCall{}, errInvalidToolsListParams
	}
	if len(params.Cursor) != 0 {
		if err := json.Unmarshal(params.Cursor, &call.Cursor); err != nil || call.Cursor == "" {
			return toolsListCall{}, errInvalidToolsListParams
		}
	}
	return call, nil
}

func (call toolsListCall) EncodeResult(ctx context.Context, tools any, nextCursor string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := toolsListResult{Tools: tools, NextCursor: nextCursor}
	encoded, err := json.Marshal(rpcSuccessEnvelope{JSONRPC: "2.0", ID: requestIDOrNull(call.ID), Result: result})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return encoded, nil
}

func encodeRPCError(ctx context.Context, id json.RawMessage, kind rpcErrorKind) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := rpcErrorFor(kind)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(rpcErrorEnvelope{JSONRPC: "2.0", ID: requestIDOrNull(id), Error: body})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return encoded, nil
}

func rpcErrorFor(kind rpcErrorKind) (rpcError, error) {
	switch kind {
	case rpcInvalidParams:
		return rpcError{Code: -32602, Message: "The tools/list parameters are invalid.", Data: &rpcErrorData{Code: "invalid_params"}}, nil
	case rpcStaleCursor:
		return rpcError{Code: -32001, Message: "The tools/list cursor is stale.", Data: &rpcErrorData{Code: "stale_cursor"}}, nil
	case rpcAuthorizationUnavailable:
		return rpcError{Code: -32002, Message: "Authorization is unavailable.", Data: &rpcErrorData{Code: "authorization_unavailable"}}, nil
	case rpcResourceLimit:
		return rpcError{Code: -32003, Message: "The tool list exceeds a resource limit.", Data: &rpcErrorData{Code: "resource_limit"}}, nil
	case rpcMethodNotFound:
		return rpcError{Code: -32601, Message: "Method not found."}, nil
	default:
		return rpcError{}, errors.New("unknown JSON-RPC error kind")
	}
}

func interceptFeatureWithCall(
	writer http.ResponseWriter,
	request *http.Request,
	wire wireRequest,
	era requestEra,
	listTools ToolsListService,
	callTools ToolsCallService,
	lease *authorization.Lease,
) bool {
	if sdkLifecycleMethod(era, wire.Method) {
		return false
	}
	if wire.Method == "tools/call" && callTools != nil {
		return interceptToolsCall(writer, request, wire, callTools, lease)
	}
	kind := rpcMethodNotFound
	if wire.Method == "tools/list" {
		call, err := parseToolsList(wire)
		if err != nil {
			kind = rpcInvalidParams
		} else if listTools != nil {
			encoded, listErr := listTools.ListTools(request.Context(), lease, call.Cursor, call.EncodeResult)
			if request.Context().Err() != nil || lease == nil || !lease.Current() {
				return true
			}
			if listErr == nil && len(encoded) != 0 {
				writeRPC(writer, encoded)
				return true
			}
			kind = rpcKindForListError(listErr)
		}
	}
	encoded, err := encodeRPCError(request.Context(), wire.ID, kind)
	if err == nil {
		writeRPC(writer, encoded)
	}
	return true
}

func rpcKindForListError(err error) rpcErrorKind {
	switch {
	case errors.Is(err, ErrToolsListInvalidParams):
		return rpcInvalidParams
	case errors.Is(err, ErrToolsListStaleCursor):
		return rpcStaleCursor
	case errors.Is(err, ErrToolsListResourceLimit):
		return rpcResourceLimit
	case errors.Is(err, ErrToolsListAuthorizationUnavailable):
		return rpcAuthorizationUnavailable
	default:
		return rpcAuthorizationUnavailable
	}
}

func writeRPC(writer http.ResponseWriter, encoded []byte) {
	writer.Header().Set("Content-Type", contract.MediaTypeJSON)
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(encoded)
}

func sdkLifecycleMethod(era requestEra, method string) bool {
	switch era {
	case eraModern:
		return method == "server/discover" || method == "ping" || method == "notifications/cancelled"
	case eraLegacyInitialize:
		return method == "initialize"
	case eraLegacyExisting:
		return method == "ping" || method == "notifications/initialized" || method == "notifications/cancelled"
	default:
		return false
	}
}

func copyRequestID(id json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), id...)
}

func requestIDOrNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return copyRequestID(id)
}
