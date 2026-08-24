// Package downstream owns bounded raw MCP JSON-RPC exchanges.
package downstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

var (
	ErrInvalidMessage   = errors.New("downstream message is invalid")
	ErrResponseMismatch = errors.New("downstream response does not match request")
	ErrTransportClosed  = errors.New("downstream transport is closed")
	ErrStopUnconfirmed  = errors.New("downstream stop is unconfirmed")
)

type Message struct {
	Payload          []byte
	Method           string
	ProtocolVersion  string
	Name             string
	SessionID        string
	ParameterHeaders map[string]string
}

type WireResponse struct {
	StatusCode  int
	ContentType string
	Body        []byte
	SessionIDs  []string
}

type Transport interface {
	Exchange(context.Context, Message) (WireResponse, error)
	Notify(context.Context, Message) (WireResponse, error)
	Close(context.Context) error
}

type Coordinator struct {
	mu        sync.Mutex
	transport Transport
	nextID    uint64
	closed    bool
}

type RPCError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type Response struct {
	Result json.RawMessage
	Error  *RPCError
}

type requestEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type notificationEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type RequestOptions struct {
	ProtocolVersion  string
	Name             string
	SessionID        string
	ParameterHeaders map[string]string
}

type responseEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func NewCoordinator(transport Transport) (*Coordinator, error) {
	if transport == nil {
		return nil, ErrInvalidMessage
	}
	return &Coordinator{transport: transport}, nil
}

func (coordinator *Coordinator) Request(ctx context.Context, method string, params json.RawMessage, protocolVersion, name string) (Response, error) {
	return coordinator.RequestWithOptions(ctx, method, params, RequestOptions{ProtocolVersion: protocolVersion, Name: name})
}

func (coordinator *Coordinator) RequestWithOptions(ctx context.Context, method string, params json.RawMessage, options RequestOptions) (Response, error) {
	requestID, wire, err := coordinator.rawRequest(ctx, method, params, options)
	if err != nil {
		return Response{}, err
	}
	if wire.StatusCode != 0 && (wire.StatusCode < 200 || wire.StatusCode > 299) {
		return Response{}, ErrInvalidMessage
	}
	if wire.StatusCode != 0 {
		mediaType, _, mediaErr := mime.ParseMediaType(wire.ContentType)
		if mediaErr != nil || mediaType != contract.MediaTypeJSON && mediaType != contract.MediaTypeEventStream {
			return Response{}, ErrInvalidMessage
		}
	}
	return decodeResponse(requestID, wire.Body)
}

func (coordinator *Coordinator) rawRequest(ctx context.Context, method string, params json.RawMessage, options RequestOptions) (uint64, WireResponse, error) {
	coordinator.mu.Lock()
	if coordinator.closed {
		coordinator.mu.Unlock()
		return 0, WireResponse{}, ErrTransportClosed
	}
	if method == "" || !validJSON(params) {
		coordinator.mu.Unlock()
		return 0, WireResponse{}, ErrInvalidMessage
	}
	coordinator.nextID++
	requestID := coordinator.nextID
	coordinator.mu.Unlock()
	payload, err := json.Marshal(requestEnvelope{JSONRPC: "2.0", ID: requestID, Method: method, Params: append(json.RawMessage(nil), params...)})
	if err != nil || int64(len(payload)) > limit("downstream_mcp_body_bytes") {
		return 0, WireResponse{}, ErrInvalidMessage
	}
	wire, err := coordinator.transport.Exchange(ctx, Message{Payload: payload, Method: method, ProtocolVersion: options.ProtocolVersion, Name: options.Name, SessionID: options.SessionID, ParameterHeaders: options.ParameterHeaders})
	return requestID, wire, err
}

func (coordinator *Coordinator) Notify(ctx context.Context, method string, params json.RawMessage, options RequestOptions) (WireResponse, error) {
	coordinator.mu.Lock()
	closed := coordinator.closed
	coordinator.mu.Unlock()
	if closed {
		return WireResponse{}, ErrTransportClosed
	}
	if method == "" || !validJSON(params) {
		return WireResponse{}, ErrInvalidMessage
	}
	payload, err := json.Marshal(notificationEnvelope{JSONRPC: "2.0", Method: method, Params: append(json.RawMessage(nil), params...)})
	if err != nil || int64(len(payload)) > limit("downstream_mcp_body_bytes") {
		return WireResponse{}, ErrInvalidMessage
	}
	return coordinator.transport.Notify(ctx, Message{Payload: payload, Method: method, ProtocolVersion: options.ProtocolVersion, Name: options.Name, SessionID: options.SessionID, ParameterHeaders: options.ParameterHeaders})
}

func decodeResponse(requestID uint64, raw []byte) (Response, error) {
	var response responseEnvelope
	if err := strictjson.Decode(raw, &response, strictjson.Options{MaxBytes: limit("downstream_mcp_body_bytes"), MaxDepth: int(limit("json_depth")), RejectUnknownMembers: true}); err != nil {
		return Response{}, ErrInvalidMessage
	}
	if response.JSONRPC != "2.0" || response.ID != requestID || (response.Error == nil) == (len(response.Result) == 0) {
		return Response{}, ErrResponseMismatch
	}
	if response.Error != nil && (response.Error.Message == "" || !validOptionalJSON(response.Error.Data)) {
		return Response{}, ErrInvalidMessage
	}
	if response.Error == nil && !validJSON(response.Result) {
		return Response{}, ErrInvalidMessage
	}
	return Response{Result: append(json.RawMessage(nil), response.Result...), Error: cloneRPCError(response.Error)}, nil
}

func (coordinator *Coordinator) Close(ctx context.Context) error {
	coordinator.mu.Lock()
	if coordinator.closed {
		coordinator.mu.Unlock()
		return nil
	}
	coordinator.closed = true
	transport := coordinator.transport
	coordinator.mu.Unlock()
	return transport.Close(ctx)
}

type StdioRuntime interface {
	Frames() <-chan []byte
	Input() io.WriteCloser
	Stop(context.Context) bool
}

type StdioTransport struct {
	mu         sync.Mutex
	exchangeMu sync.Mutex
	runtime    StdioRuntime
	closed     bool
}

func NewStdioTransport(runtime StdioRuntime) (*StdioTransport, error) {
	if runtime == nil {
		return nil, ErrInvalidMessage
	}
	return &StdioTransport{runtime: runtime}, nil
}

func (transport *StdioTransport) Exchange(ctx context.Context, message Message) (WireResponse, error) {
	transport.exchangeMu.Lock()
	defer transport.exchangeMu.Unlock()
	if err := transport.writeMessage(message); err != nil {
		return WireResponse{}, err
	}
	select {
	case frame, ok := <-transport.runtime.Frames():
		if !ok || int64(len(frame)) > limit("stdio_protocol_frame_bytes") {
			return WireResponse{}, ErrTransportClosed
		}
		return WireResponse{Body: append([]byte(nil), frame...)}, nil
	case <-ctx.Done():
		return WireResponse{}, ctx.Err()
	}
}

func (transport *StdioTransport) Notify(_ context.Context, message Message) (WireResponse, error) {
	transport.exchangeMu.Lock()
	defer transport.exchangeMu.Unlock()
	if err := transport.writeMessage(message); err != nil {
		return WireResponse{}, err
	}
	return WireResponse{}, nil
}

func (transport *StdioTransport) writeMessage(message Message) error {
	transport.mu.Lock()
	closed := transport.closed
	transport.mu.Unlock()
	if closed || int64(len(message.Payload)) > limit("stdio_protocol_frame_bytes") || bytes.ContainsRune(message.Payload, '\n') {
		return ErrTransportClosed
	}
	if _, err := transport.runtime.Input().Write(append(append([]byte(nil), message.Payload...), '\n')); err != nil {
		return ErrTransportClosed
	}
	return nil
}

func (transport *StdioTransport) Close(ctx context.Context) error {
	transport.mu.Lock()
	if transport.closed {
		transport.mu.Unlock()
		return nil
	}
	transport.closed = true
	transport.mu.Unlock()
	if !transport.runtime.Stop(ctx) {
		return ErrStopUnconfirmed
	}
	return nil
}

type HTTPTransport struct {
	mu         sync.Mutex
	factory    *remote.Factory
	endpoint   remote.Endpoint
	auth       string
	rootCtx    context.Context
	rootCancel context.CancelFunc
	closed     bool
}

func NewHTTPTransport(factory *remote.Factory, endpoint remote.Endpoint, authorization string) (*HTTPTransport, error) {
	if factory == nil || (authorization != "" && !strings.HasPrefix(authorization, "Bearer ")) {
		return nil, ErrInvalidMessage
	}
	rootCtx, rootCancel := context.WithCancel(context.Background())
	return &HTTPTransport{factory: factory, endpoint: endpoint, auth: authorization, rootCtx: rootCtx, rootCancel: rootCancel}, nil
}

func (transport *HTTPTransport) Exchange(ctx context.Context, message Message) (WireResponse, error) {
	return transport.exchange(ctx, message)
}

func (transport *HTTPTransport) Notify(ctx context.Context, message Message) (WireResponse, error) {
	return transport.exchange(ctx, message)
}

func (transport *HTTPTransport) exchange(ctx context.Context, message Message) (WireResponse, error) {
	transport.mu.Lock()
	if transport.closed {
		transport.mu.Unlock()
		return WireResponse{}, ErrTransportClosed
	}
	exchangeCtx, cancel := context.WithCancel(ctx)
	stopRootCancel := context.AfterFunc(transport.rootCtx, cancel)
	transport.mu.Unlock()
	defer func() {
		stopRootCancel()
		cancel()
	}()
	header := make(http.Header)
	header.Set("Content-Type", contract.MediaTypeJSON)
	header.Set("Accept", contract.MediaTypeJSON+", "+contract.MediaTypeEventStream)
	header["User-Agent"] = []string{""}
	if message.ProtocolVersion != "" {
		header.Set("MCP-Protocol-Version", message.ProtocolVersion)
	}
	header.Set("Mcp-Method", message.Method)
	if message.Name != "" {
		header.Set("Mcp-Name", message.Name)
	}
	if transport.auth != "" {
		header.Set("Authorization", transport.auth)
	}
	if message.SessionID != "" {
		header.Set("Mcp-Session-Id", message.SessionID)
	}
	seenParameters := make(map[string]struct{}, len(message.ParameterHeaders))
	for name, value := range message.ParameterHeaders {
		folded := strings.ToLower(name)
		if !headerToken.MatchString(name) || strings.ContainsAny(value, "\r\n") || int64(len(value)) > limit("request_header_value_bytes") {
			return WireResponse{}, ErrInvalidMessage
		}
		if _, duplicate := seenParameters[folded]; duplicate {
			return WireResponse{}, ErrInvalidMessage
		}
		seenParameters[folded] = struct{}{}
		header.Set("Mcp-Param-"+name, value)
	}
	response, err := transport.factory.Open(exchangeCtx, remote.Request{Endpoint: transport.endpoint, Method: http.MethodPost, Header: header, Body: message.Payload, MaxBody: limit("downstream_mcp_body_bytes")})
	if err != nil {
		return WireResponse{}, err
	}
	defer func() { _ = response.Body.Close() }()
	contentType := response.Header.Get("Content-Type")
	mediaType, _, mediaErr := mime.ParseMediaType(contentType)
	var body []byte
	if mediaErr == nil && mediaType == contract.MediaTypeEventStream {
		body, err = parseSSEReader(response.Body)
	} else {
		body, err = readBounded(response.Body, limit("downstream_mcp_body_bytes"))
	}
	if err != nil {
		return WireResponse{}, err
	}
	return WireResponse{StatusCode: response.StatusCode, ContentType: contentType, Body: body, SessionIDs: append([]string(nil), response.Header.Values("Mcp-Session-Id")...)}, nil
}

func (transport *HTTPTransport) Close(context.Context) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.closed = true
	transport.rootCancel()
	return nil
}

func parseSSE(contents []byte) ([]byte, error) {
	return parseSSEReader(bytes.NewReader(contents))
}

func parseSSEReader(reader io.Reader) ([]byte, error) {
	maximum := limit("downstream_sse_event_bytes")
	scanner := bufio.NewScanner(io.LimitReader(reader, maximum+1))
	scanner.Buffer(make([]byte, 4096), int(maximum)+1)
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		} else if !strings.HasPrefix(line, ":") && !strings.HasPrefix(line, "event:") && !strings.HasPrefix(line, "id:") && !strings.HasPrefix(line, "retry:") {
			return nil, ErrInvalidMessage
		}
	}
	if scanner.Err() != nil || len(data) == 0 {
		return nil, ErrInvalidMessage
	}
	result := []byte(strings.Join(data, "\n"))
	if !validJSON(result) {
		return nil, ErrInvalidMessage
	}
	return result, nil
}

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil || int64(len(contents)) > maximum {
		return nil, ErrInvalidMessage
	}
	return contents, nil
}

func validJSON(value []byte) bool {
	return len(value) != 0 && strictjson.Decode(value, new(any), strictjson.Options{MaxBytes: limit("downstream_mcp_body_bytes"), MaxDepth: int(limit("json_depth"))}) == nil
}

func validOptionalJSON(value []byte) bool { return len(value) == 0 || validJSON(value) }

func cloneRPCError(value *RPCError) *RPCError {
	if value == nil {
		return nil
	}
	return &RPCError{Code: value.Code, Message: value.Message, Data: append(json.RawMessage(nil), value.Data...)}
}

var headerToken = regexp.MustCompile(`^[!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+$`)

func limit(name string) int64 {
	value, ok := contract.FixedLimitByName(name)
	if !ok {
		panic("missing fixed limit " + strconv.Quote(name))
	}
	return value.Maximum
}
