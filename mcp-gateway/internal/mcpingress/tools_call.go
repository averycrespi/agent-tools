package mcpingress

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

var (
	errInvalidToolsCallID = errors.New("tools/call request ID is invalid")
	toolsCallIDPattern    = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
)

type ToolsCallRequest struct {
	Params    strictjson.Value
	WireValid bool
}

type ToolsCallResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
	IsError           *bool             `json:"isError,omitempty"`
}

type ToolsCallResponse struct {
	Result       *ToolsCallResult
	ErrorCode    contract.AgentCallErrorCode
	InvocationID string
}

type ToolsCallService interface {
	Call(context.Context, *authorization.Lease, ToolsCallRequest) ToolsCallResponse
}

type toolsCall struct {
	ID           json.RawMessage
	Request      ToolsCallRequest
	Notification bool
}

type toolsCallSuccessEnvelope struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id"`
	Result  *ToolsCallResult `json:"result"`
}

type toolsCallErrorEnvelope struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id"`
	Error   toolsCallRPCError `json:"error"`
}

type toolsCallRPCError struct {
	Code    int                         `json:"code"`
	Message string                      `json:"message"`
	Data    contract.AgentCallErrorData `json:"data"`
}

func parseToolsCall(wire wireRequest) (toolsCall, error) {
	call := toolsCall{ID: copyRequestID(wire.ID)}
	if len(wire.ID) == 0 {
		call.Notification = true
		return call, nil
	}
	if !validToolsCallID(wire.ID) {
		return toolsCall{}, errInvalidToolsCallID
	}
	call.Request.WireValid = true
	if len(wire.Params) == 0 {
		return call, nil
	}
	params, err := strictjson.ParseValue(wire.Params, strictjson.Options{
		MaxBytes: int64(limit("mcp_body_bytes")), MaxDepth: limit("json_depth"),
	})
	if err != nil {
		call.Request.WireValid = false
		return call, nil
	}
	if params.Type != strictjson.ValueObject {
		call.Request.Params = params
		return call, nil
	}
	clean := strictjson.Value{Type: strictjson.ValueObject, Object: make([]strictjson.Member, 0, len(params.Object))}
	for _, member := range params.Object {
		switch member.Name {
		case "name", "arguments":
			clean.Object = append(clean.Object, member)
		case "_meta":
			if member.Value.Type != strictjson.ValueObject {
				call.Request.WireValid = false
			}
		default:
			call.Request.WireValid = false
		}
	}
	call.Request.Params = clean
	return call, nil
}

func validToolsCallID(id json.RawMessage) bool {
	value, err := strictjson.ParseValue(id, strictjson.Options{
		MaxBytes: int64(limit("mcp_body_bytes")), MaxDepth: limit("json_depth"),
	})
	return err == nil && (value.Type == strictjson.ValueString || value.Type == strictjson.ValueNumber)
}

func interceptToolsCall(
	writer http.ResponseWriter,
	request *http.Request,
	wire wireRequest,
	service ToolsCallService,
	lease *authorization.Lease,
) bool {
	call, err := parseToolsCall(wire)
	if err != nil {
		return true
	}
	if call.Notification {
		writer.WriteHeader(http.StatusAccepted)
		return true
	}
	response := service.Call(request.Context(), lease, call.Request)
	encoded, err := encodeToolsCallResponse(request.Context(), call.ID, response)
	if err == nil {
		writeRPC(writer, encoded)
	}
	return true
}

func encodeToolsCallResponse(ctx context.Context, id json.RawMessage, response ToolsCallResponse) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if response.Result != nil && response.ErrorCode == "" && response.InvocationID == "" && response.Result.Content != nil {
		encoded, err := json.Marshal(toolsCallSuccessEnvelope{JSONRPC: "2.0", ID: copyRequestID(id), Result: response.Result})
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return encoded, nil
	}
	callError, ok := contract.AgentCallErrorForCode(response.ErrorCode)
	validID := response.InvocationID == "" || toolsCallIDPattern.MatchString(response.InvocationID)
	validCombination := ok && response.Result == nil && validID &&
		(response.ErrorCode == contract.AuditUnavailable && response.InvocationID == "" || response.ErrorCode != contract.AuditUnavailable && response.InvocationID != "")
	if !validCombination {
		callError, _ = contract.AgentCallErrorForCode(contract.AuditUnavailable)
		response = ToolsCallResponse{ErrorCode: contract.AuditUnavailable}
	}
	var invocationID *string
	if response.InvocationID != "" {
		value := response.InvocationID
		invocationID = &value
	}
	data := contract.AgentCallErrorData{
		Code: response.ErrorCode, InvocationID: invocationID,
		OutcomeUnknown: response.ErrorCode == contract.OutcomeUnknown,
	}
	encoded, err := json.Marshal(toolsCallErrorEnvelope{
		JSONRPC: "2.0", ID: copyRequestID(id),
		Error: toolsCallRPCError{Code: contract.AgentCallJSONRPCErrorCode, Message: callError.Message, Data: data},
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return encoded, nil
}
