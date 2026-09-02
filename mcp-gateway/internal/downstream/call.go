package downstream

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

var ErrCallConsumed = errors.New("downstream call is already consumed")

type FailureClass string

const (
	FailurePreStart        FailureClass = "pre_start"
	FailureResponseInvalid FailureClass = "response_invalid"
	FailureStartUncertain  FailureClass = "start_uncertain"
)

type CallResult struct {
	Response Response
	Failure  FailureClass
	Err      error
}

type Call struct {
	mu               sync.Mutex
	runtime          *Runtime
	upstreamName     string
	params           json.RawMessage
	used             bool
	done             bool
	cancelRequested  bool
	cancellationSent bool
	handedOff        bool
	requestID        uint64
	parameterHeaders map[string]string
	cancelLocal      context.CancelFunc
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (runtime *Runtime) NewCall(upstreamName string, validatedArguments json.RawMessage) (*Call, error) {
	return runtime.NewCallWithHeaders(upstreamName, validatedArguments, nil)
}

func (runtime *Runtime) NewCallWithHeaders(upstreamName string, validatedArguments json.RawMessage, parameterHeaders map[string]string) (*Call, error) {
	if upstreamName == "" || !utf8.ValidString(upstreamName) || int64(len(upstreamName)) > limit("external_tool_name_bytes") || !jsonObject(validatedArguments) {
		return nil, ErrInvalidMessage
	}
	runtime.mu.Lock()
	closed := runtime.closed
	runtime.mu.Unlock()
	if closed {
		return nil, ErrTransportClosed
	}
	params, err := json.Marshal(toolsCallParams{Name: upstreamName, Arguments: append(json.RawMessage(nil), validatedArguments...)})
	if err != nil || int64(len(params)) > limit("downstream_mcp_body_bytes") {
		return nil, ErrInvalidMessage
	}
	return &Call{runtime: runtime, upstreamName: upstreamName, params: params, parameterHeaders: cloneParameterHeaders(parameterHeaders)}, nil
}

func (call *Call) Execute(ctx context.Context) CallResult {
	call.mu.Lock()
	if call.used {
		call.mu.Unlock()
		return CallResult{Failure: FailurePreStart, Err: ErrCallConsumed}
	}
	call.used = true
	if call.cancelRequested || ctx.Err() != nil {
		call.done = true
		call.mu.Unlock()
		return CallResult{Failure: FailurePreStart, Err: context.Canceled}
	}
	executeCtx, cancel := call.runtime.callDeadline(ctx, contract.MaximumDownstreamCallDeadline)
	call.cancelLocal = cancel
	call.mu.Unlock()
	if !call.runtime.registerCall(call) {
		cancel()
		call.finish(false)
		return CallResult{Failure: FailurePreStart, Err: ErrTransportClosed}
	}
	defer call.runtime.unregisterCall(call)
	stopCancellationWatch := context.AfterFunc(executeCtx, func() {
		_ = call.requestCancellation(context.Background())
	})
	response, completeResponse, err := call.perform(executeCtx)
	if executeCtx.Err() != nil {
		_ = call.requestCancellation(context.Background())
		if err == nil {
			err = executeCtx.Err()
			completeResponse = false
		}
	}
	handedOff := call.finish(true)
	stopCancellationWatch()
	cancel()
	if err != nil {
		failure := FailurePreStart
		if completeResponse {
			failure = FailureResponseInvalid
		} else if handedOff {
			failure = FailureStartUncertain
		}
		return CallResult{Failure: failure, Err: err}
	}
	return CallResult{Response: response}
}

func (call *Call) Cancel(ctx context.Context) error {
	return call.requestCancellation(ctx)
}

func (call *Call) perform(ctx context.Context) (Response, bool, error) {
	call.runtime.mu.Lock()
	era := call.runtime.era
	sessionID := call.runtime.sessionID
	coordinator := call.runtime.coordinator
	closed := call.runtime.closed
	call.runtime.mu.Unlock()
	if closed {
		return Response{}, false, ErrTransportClosed
	}
	params := append(json.RawMessage(nil), call.params...)
	version := contract.LegacyProtocolVersion
	if era == EraModern {
		version = contract.ModernProtocolVersion
		var err error
		params, err = addModernMetadata(params)
		if err != nil {
			return Response{}, false, err
		}
	}
	requestID, wire, err := coordinator.rawRequest(ctx, "tools/call", params, RequestOptions{
		ProtocolVersion:  version,
		Name:             call.upstreamName,
		SessionID:        sessionID,
		RequestID:        call.setRequestID,
		ParameterHeaders: cloneParameterHeaders(call.parameterHeaders),
		MarkHandoff:      call.markHandoff,
	})
	if err != nil {
		return Response{}, false, err
	}
	if !runtimeSessionCurrent(era, sessionID, wire) {
		_ = call.runtime.Close(context.Background())
		return Response{}, false, ErrSessionLost
	}
	response, err := decodeNegotiationResponse(requestID, wire)
	return response, true, err
}

func cloneParameterHeaders(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]string, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}

func (call *Call) setRequestID(requestID uint64) {
	call.mu.Lock()
	call.requestID = requestID
	call.mu.Unlock()
}

func (call *Call) markHandoff() {
	call.mu.Lock()
	call.handedOff = true
	call.mu.Unlock()
}

func (call *Call) requestCancellation(ctx context.Context) error {
	call.mu.Lock()
	if call.done {
		call.mu.Unlock()
		return nil
	}
	call.cancelRequested = true
	cancel := call.cancelLocal
	send := call.handedOff && !call.cancellationSent && call.shouldNotify()
	if send {
		call.cancellationSent = true
	}
	call.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if send {
		return call.sendCancellation(ctx)
	}
	return nil
}

func (call *Call) finish(sendPendingCancellation bool) bool {
	call.mu.Lock()
	handedOff := call.handedOff
	send := sendPendingCancellation && call.cancelRequested && handedOff && !call.cancellationSent && call.shouldNotify()
	if send {
		call.cancellationSent = true
	}
	call.done = true
	call.mu.Unlock()
	if send {
		_ = call.sendCancellation(context.Background())
	}
	return handedOff
}

func (call *Call) shouldNotify() bool {
	return call.runtime.coordinator.Kind() != TransportHTTP || call.runtime.era == EraLegacy
}

func (call *Call) sendCancellation(ctx context.Context) error {
	call.mu.Lock()
	requestID := call.requestID
	call.mu.Unlock()
	params, err := json.Marshal(struct {
		RequestID uint64 `json:"requestId"`
	}{RequestID: requestID})
	if err != nil {
		return ErrInvalidMessage
	}
	options := RequestOptions{ProtocolVersion: contract.LegacyProtocolVersion, SessionID: call.runtime.sessionID}
	if call.runtime.era == EraModern {
		params, err = addModernMetadata(params)
		if err != nil {
			return err
		}
		options.ProtocolVersion = contract.ModernProtocolVersion
	}
	_, err = call.runtime.coordinator.Notify(ctx, "notifications/cancelled", params, options)
	return err
}
