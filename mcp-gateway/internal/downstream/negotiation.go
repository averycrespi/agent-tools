package downstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

var (
	ErrUnsupportedProtocol = errors.New("downstream protocol is unsupported")
	ErrFallbackRejected    = errors.New("downstream fallback evidence is rejected")
	ErrSessionLost         = errors.New("downstream session is lost")
)

type Mode string

const (
	ModeModern Mode = "modern"
	ModeLegacy Mode = "legacy"
	ModeAuto   Mode = "auto"
)

type Era string

const (
	EraModern Era = "modern"
	EraLegacy Era = "legacy"
)

const (
	downstreamClientName    = "mcp-gateway"
	downstreamClientVersion = "s2"
)

type OpenCoordinator func(context.Context) (*Coordinator, error)
type DeadlineFunc func(context.Context, time.Duration) (context.Context, context.CancelFunc)

type Negotiator struct {
	open     OpenCoordinator
	deadline DeadlineFunc
}

type Runtime struct {
	mu           sync.Mutex
	era          Era
	coordinator  *Coordinator
	sessionID    string
	activeCalls  map[*Call]struct{}
	callDeadline DeadlineFunc
	closed       bool
}

type clientImplementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type modernMeta struct {
	ProtocolVersion string               `json:"io.modelcontextprotocol/protocolVersion"`
	ClientInfo      clientImplementation `json:"io.modelcontextprotocol/clientInfo"`
	Capabilities    struct{}             `json:"io.modelcontextprotocol/clientCapabilities"`
}

type modernParams struct {
	Meta modernMeta `json:"_meta"`
}

type initializeParams struct {
	ProtocolVersion string               `json:"protocolVersion"`
	Capabilities    struct{}             `json:"capabilities"`
	ClientInfo      clientImplementation `json:"clientInfo"`
}

type discoverResult struct {
	ResultType        string                     `json:"resultType,omitempty"`
	Meta              map[string]json.RawMessage `json:"_meta,omitempty"`
	TTLMs             *int64                     `json:"ttlMs"`
	CacheScope        *string                    `json:"cacheScope"`
	SupportedVersions *[]string                  `json:"supportedVersions"`
	Capabilities      json.RawMessage            `json:"capabilities"`
	Instructions      string                     `json:"instructions,omitempty"`
}

type unsupportedVersionData struct {
	Supported []string `json:"supported"`
	Requested string   `json:"requested"`
}

type initializeResult struct {
	Meta            map[string]json.RawMessage `json:"_meta,omitempty"`
	Capabilities    json.RawMessage            `json:"capabilities"`
	Instructions    string                     `json:"instructions,omitempty"`
	ProtocolVersion string                     `json:"protocolVersion"`
	ServerInfo      json.RawMessage            `json:"serverInfo"`
}

type serverImplementation struct {
	Name        string            `json:"name"`
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	Version     string            `json:"version"`
	WebsiteURL  string            `json:"websiteUrl,omitempty"`
	Icons       []json.RawMessage `json:"icons,omitempty"`
}

func NewNegotiator(open OpenCoordinator) (*Negotiator, error) {
	return NewNegotiatorWithDeadline(open, context.WithTimeout)
}

func NewNegotiatorWithDeadline(open OpenCoordinator, deadline DeadlineFunc) (*Negotiator, error) {
	if open == nil || deadline == nil {
		return nil, ErrInvalidMessage
	}
	return &Negotiator{open: open, deadline: deadline}, nil
}

func (negotiator *Negotiator) Negotiate(ctx context.Context, mode Mode) (*Runtime, error) {
	if mode != ModeModern && mode != ModeLegacy && mode != ModeAuto {
		return nil, ErrUnsupportedProtocol
	}
	initializationCtx, cancel := negotiator.deadline(ctx, contract.DownstreamInitializationDeadline)
	defer cancel()
	coordinator, err := negotiator.open(initializationCtx)
	if err != nil {
		return nil, err
	}
	if coordinator == nil {
		return nil, ErrInvalidMessage
	}
	if mode == ModeLegacy {
		return negotiateLegacy(initializationCtx, coordinator)
	}
	selected, fallback, err := negotiateModern(initializationCtx, coordinator)
	if err != nil {
		_ = coordinator.Close(initializationCtx)
		return nil, err
	}
	if selected {
		return newRuntime(EraModern, coordinator, ""), nil
	}
	if mode != ModeAuto || !fallback {
		_ = coordinator.Close(initializationCtx)
		return nil, ErrFallbackRejected
	}
	if err := coordinator.Close(initializationCtx); err != nil {
		return nil, err
	}
	legacyCoordinator, err := negotiator.open(initializationCtx)
	if err != nil {
		return nil, err
	}
	if legacyCoordinator == nil || legacyCoordinator == coordinator {
		return nil, ErrFallbackRejected
	}
	return negotiateLegacy(initializationCtx, legacyCoordinator)
}

func negotiateModern(ctx context.Context, coordinator *Coordinator) (bool, bool, error) {
	params, _ := json.Marshal(modernParams{Meta: newModernMeta()})
	requestID, wire, err := coordinator.rawRequest(ctx, "server/discover", params, RequestOptions{ProtocolVersion: contract.ModernProtocolVersion})
	if err != nil {
		return false, false, err
	}
	if len(wire.SessionIDs) != 0 {
		return false, false, ErrSessionLost
	}
	if isTextFallback(wire) {
		return false, true, nil
	}
	response, err := decodeNegotiationResponse(requestID, wire)
	if err != nil {
		return false, false, err
	}
	if response.Error == nil {
		if err := validateDiscoverResult(response.Result); err != nil {
			return false, false, err
		}
		return true, false, nil
	}
	if response.Error.Code == -32601 && isJSONFallback(wire) && nullOrAbsent(response.Error.Data) {
		return false, true, nil
	}
	if response.Error.Code != -32022 {
		return false, false, ErrFallbackRejected
	}
	if err := validateUnsupportedVersion(response.Error.Data, contract.ModernProtocolVersion); err != nil {
		return false, false, err
	}
	requestID, wire, err = coordinator.rawRequest(ctx, "server/discover", params, RequestOptions{ProtocolVersion: contract.ModernProtocolVersion})
	if err != nil {
		return false, false, err
	}
	if len(wire.SessionIDs) != 0 {
		return false, false, ErrSessionLost
	}
	response, err = decodeNegotiationResponse(requestID, wire)
	if err != nil || response.Error != nil {
		return false, false, ErrUnsupportedProtocol
	}
	if err := validateDiscoverResult(response.Result); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func negotiateLegacy(ctx context.Context, coordinator *Coordinator) (*Runtime, error) {
	params, _ := json.Marshal(initializeParams{ProtocolVersion: contract.LegacyProtocolVersion, ClientInfo: downstreamClientInfo()})
	requestID, wire, err := coordinator.rawRequest(ctx, "initialize", params, RequestOptions{ProtocolVersion: contract.LegacyProtocolVersion})
	if err != nil {
		_ = coordinator.Close(ctx)
		return nil, err
	}
	response, err := decodeNegotiationResponse(requestID, wire)
	if err != nil || response.Error != nil {
		_ = coordinator.Close(ctx)
		return nil, ErrUnsupportedProtocol
	}
	if err := validateInitializeResult(response.Result); err != nil {
		_ = coordinator.Close(ctx)
		return nil, err
	}
	sessionID, err := initialSession(wire.SessionIDs)
	if err != nil {
		_ = coordinator.Close(ctx)
		return nil, err
	}
	notification, err := coordinator.Notify(ctx, "notifications/initialized", json.RawMessage(`{}`), RequestOptions{ProtocolVersion: contract.LegacyProtocolVersion, SessionID: sessionID})
	if err != nil || !successfulNotification(notification) || !sameSession(sessionID, notification.SessionIDs) {
		_ = coordinator.Close(ctx)
		if err != nil {
			return nil, err
		}
		return nil, ErrSessionLost
	}
	return newRuntime(EraLegacy, coordinator, sessionID), nil
}

func newRuntime(era Era, coordinator *Coordinator, sessionID string) *Runtime {
	return &Runtime{era: era, coordinator: coordinator, sessionID: sessionID, activeCalls: make(map[*Call]struct{}), callDeadline: context.WithTimeout}
}

func (runtime *Runtime) Era() Era {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.era
}

func (runtime *Runtime) SessionID() string {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.sessionID
}

func (runtime *Runtime) Request(ctx context.Context, method string, params json.RawMessage, name string) (Response, error) {
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		return Response{}, ErrTransportClosed
	}
	era := runtime.era
	sessionID := runtime.sessionID
	coordinator := runtime.coordinator
	runtime.mu.Unlock()
	if era == EraModern {
		var err error
		params, err = addModernMetadata(params)
		if err != nil {
			return Response{}, err
		}
	} else if err := validateLegacyParams(params); err != nil {
		return Response{}, err
	}
	version := contract.LegacyProtocolVersion
	if era == EraModern {
		version = contract.ModernProtocolVersion
	}
	requestID, wire, err := coordinator.rawRequest(ctx, method, params, RequestOptions{ProtocolVersion: version, Name: name, SessionID: sessionID})
	if err != nil {
		return Response{}, err
	}
	if !runtimeSessionCurrent(era, sessionID, wire) {
		_ = runtime.Close(ctx)
		return Response{}, ErrSessionLost
	}
	return decodeNegotiationResponse(requestID, wire)
}

func (runtime *Runtime) Close(ctx context.Context) error {
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		return nil
	}
	runtime.closed = true
	coordinator := runtime.coordinator
	calls := make([]*Call, 0, len(runtime.activeCalls))
	for call := range runtime.activeCalls {
		calls = append(calls, call)
	}
	runtime.mu.Unlock()
	for _, call := range calls {
		_ = call.requestCancellation(ctx)
	}
	return coordinator.Close(ctx)
}

func (runtime *Runtime) registerCall(call *Call) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed {
		return false
	}
	runtime.activeCalls[call] = struct{}{}
	return true
}

func (runtime *Runtime) unregisterCall(call *Call) {
	runtime.mu.Lock()
	delete(runtime.activeCalls, call)
	runtime.mu.Unlock()
}

func newModernMeta() modernMeta {
	return modernMeta{ProtocolVersion: contract.ModernProtocolVersion, ClientInfo: downstreamClientInfo()}
}

func downstreamClientInfo() clientImplementation {
	return clientImplementation{Name: downstreamClientName, Version: downstreamClientVersion}
}

func addModernMetadata(params json.RawMessage) (json.RawMessage, error) {
	var members map[string]json.RawMessage
	if err := strictjson.Decode(params, &members, strictjson.Options{MaxBytes: limit("downstream_mcp_body_bytes"), MaxDepth: int(limit("json_depth"))}); err != nil || members == nil {
		return nil, ErrInvalidMessage
	}
	if _, inherited := members["_meta"]; inherited {
		return nil, ErrInvalidMessage
	}
	metadata, _ := json.Marshal(newModernMeta())
	members["_meta"] = metadata
	result, err := json.Marshal(members)
	if err != nil || int64(len(result)) > limit("downstream_mcp_body_bytes") {
		return nil, ErrInvalidMessage
	}
	return result, nil
}

func validateLegacyParams(params json.RawMessage) error {
	var members map[string]json.RawMessage
	if err := strictjson.Decode(params, &members, strictjson.Options{MaxBytes: limit("downstream_mcp_body_bytes"), MaxDepth: int(limit("json_depth"))}); err != nil || members == nil {
		return ErrInvalidMessage
	}
	if _, metadata := members["_meta"]; metadata {
		return ErrInvalidMessage
	}
	return nil
}

func decodeNegotiationResponse(requestID uint64, wire WireResponse) (Response, error) {
	if wire.StatusCode != 0 {
		if wire.StatusCode < 200 || wire.StatusCode > 299 {
			return Response{}, ErrFallbackRejected
		}
		mediaType, _, err := mime.ParseMediaType(wire.ContentType)
		if err != nil || mediaType != contract.MediaTypeJSON && mediaType != contract.MediaTypeEventStream {
			return Response{}, ErrFallbackRejected
		}
	}
	return decodeResponse(requestID, wire.Body)
}

func validateDiscoverResult(raw json.RawMessage) error {
	var result discoverResult
	if err := strictjson.Decode(raw, &result, strictjson.Options{MaxBytes: limit("downstream_mcp_body_bytes"), MaxDepth: int(limit("json_depth")), RejectUnknownMembers: true}); err != nil {
		return ErrUnsupportedProtocol
	}
	if result.ResultType != "" && result.ResultType != "complete" || result.TTLMs == nil || *result.TTLMs < 0 || result.CacheScope == nil || (*result.CacheScope != "public" && *result.CacheScope != "private") || result.SupportedVersions == nil || !containsExactVersion(*result.SupportedVersions, contract.ModernProtocolVersion) || !jsonObject(result.Capabilities) {
		return ErrUnsupportedProtocol
	}
	return nil
}

func validateUnsupportedVersion(raw json.RawMessage, requested string) error {
	var data unsupportedVersionData
	if err := strictjson.Decode(raw, &data, strictjson.Options{MaxBytes: limit("downstream_mcp_body_bytes"), MaxDepth: int(limit("json_depth")), RejectUnknownMembers: true}); err != nil || data.Requested != requested || !containsExactVersion(data.Supported, contract.ModernProtocolVersion) {
		return ErrUnsupportedProtocol
	}
	return nil
}

func validateInitializeResult(raw json.RawMessage) error {
	var result initializeResult
	if err := strictjson.Decode(raw, &result, strictjson.Options{MaxBytes: limit("downstream_mcp_body_bytes"), MaxDepth: int(limit("json_depth")), RejectUnknownMembers: true}); err != nil || result.ProtocolVersion != contract.LegacyProtocolVersion || !jsonObject(result.Capabilities) || !validServerImplementation(result.ServerInfo) {
		return ErrUnsupportedProtocol
	}
	return nil
}

func validServerImplementation(raw json.RawMessage) bool {
	var implementation serverImplementation
	return strictjson.Decode(raw, &implementation, strictjson.Options{MaxBytes: limit("downstream_mcp_body_bytes"), MaxDepth: int(limit("json_depth")), RejectUnknownMembers: true}) == nil && implementation.Name != "" && implementation.Version != ""
}

func jsonObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' && validJSON(trimmed)
}

func containsExactVersion(values []string, expected string) bool {
	if len(values) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	found := false
	for _, value := range values {
		if value == "" {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
		found = found || value == expected
	}
	return found
}

func isJSONFallback(wire WireResponse) bool {
	if wire.StatusCode == 0 {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(wire.ContentType)
	return err == nil && wire.StatusCode == 200 && mediaType == contract.MediaTypeJSON
}

func isTextFallback(wire WireResponse) bool {
	if wire.StatusCode != 400 {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(wire.ContentType)
	if err != nil || mediaType != "text/plain" {
		return false
	}
	return bytes.Equal(wire.Body, []byte("JSON RPC not handled: \"server/discover\" unsupported\n")) || bytes.Equal(wire.Body, []byte("Bad Request: Unsupported protocol version\n"))
}

func nullOrAbsent(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}

func initialSession(values []string) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 || values[0] == "" || int64(len(values[0])) > limit("downstream_legacy_session_id_bytes") {
		return "", ErrSessionLost
	}
	return values[0], nil
}

func successfulNotification(wire WireResponse) bool {
	return wire.StatusCode == 0 || wire.StatusCode >= 200 && wire.StatusCode <= 299
}

func sameSession(bound string, values []string) bool {
	if len(values) == 0 {
		return true
	}
	return len(values) == 1 && bound != "" && values[0] == bound
}

func runtimeSessionCurrent(era Era, bound string, wire WireResponse) bool {
	if era == EraModern {
		return len(wire.SessionIDs) == 0
	}
	if wire.StatusCode == 404 && bound != "" {
		return false
	}
	return sameSession(bound, wire.SessionIDs)
}
