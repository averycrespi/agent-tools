package downstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
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
	closeMu      sync.Mutex
	era          Era
	coordinator  *Coordinator
	sessionID    string
	activeCalls  map[*Call]struct{}
	callDeadline DeadlineFunc
	closed       bool
	closeDone    bool
	closeErr     error
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

type legacyVersionError struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Error   struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data,omitempty"`
	} `json:"error"`
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

func (negotiator *Negotiator) Negotiate(ctx context.Context, mode Mode) (result *Runtime, resultErr error) {
	fallbackState := "not_attempted"
	defer func() {
		if resultErr != nil {
			detail := diagnostics.Snapshot("downstream", "negotiate", "", resultErr)
			detail.Explanation = fmt.Sprintf("configured_mode=%s legacy_fallback=%s initialization_budget_ms=%d deadline_provenance=unknown; %s", diagnostics.Text(string(mode), 16), fallbackState, contract.DownstreamInitializationDeadline.Milliseconds(), detail.Explanation)
			resultErr = diagnostics.WithDetail(resultErr, detail)
		}
	}()
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
		var challenge *OAuthChallengeDisposition
		if !errors.As(err, &challenge) {
			err = errors.Join(err, coordinator.Close(initializationCtx))
		}
		return nil, err
	}
	if selected {
		return newRuntime(EraModern, coordinator, ""), nil
	}
	if mode != ModeAuto || !fallback {
		fallbackState = "denied_by_mode_or_evidence"
		return nil, errors.Join(negotiationPredicate(ErrFallbackRejected, "fallback", "mode_and_evidence", fmt.Sprintf("evidence_allows=%t", fallback)), coordinator.Close(initializationCtx))
	}
	fallbackState = "probe_close_required"
	if err := coordinator.Close(initializationCtx); err != nil {
		return nil, err
	}
	fallbackState = "fresh_legacy_attempt"
	legacyCoordinator, err := negotiator.open(initializationCtx)
	if err != nil {
		return nil, err
	}
	if legacyCoordinator == nil || legacyCoordinator == coordinator {
		return nil, ErrFallbackRejected
	}
	return negotiateLegacy(initializationCtx, legacyCoordinator)
}

func negotiateModern(ctx context.Context, coordinator *Coordinator) (selected bool, fallback bool, resultErr error) {
	var wire WireResponse
	attempt := 1
	var rpcCode int64
	defer func() {
		resultErr = negotiationContext(resultErr, "server/discover", contract.ModernProtocolVersion, attempt, wire, rpcCode)
	}()
	params, _ := json.Marshal(modernParams{Meta: newModernMeta()})
	requestID, wire, err := coordinator.rawRequest(ctx, "server/discover", params, RequestOptions{ProtocolVersion: contract.ModernProtocolVersion})
	if err != nil {
		return false, false, err
	}
	if wire.OAuthChallenge != nil {
		return false, false, wire.OAuthChallenge.at(OAuthChallengeModernDiscovery)
	}
	if len(wire.SessionIDs) != 0 {
		return false, false, negotiationPredicate(ErrSessionLost, "session_header", "modern_stateless", fmt.Sprintf("observed=%d expected=0", len(wire.SessionIDs)))
	}
	if isTextFallback(wire) || isLegacyVersionFallback(wire) {
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
	rpcCode = response.Error.Code
	if response.Error.Code == -32601 && isJSONFallback(wire) && nullOrAbsent(response.Error.Data) {
		return false, true, nil
	}
	if response.Error.Code != -32022 {
		return false, false, negotiationPredicate(ErrFallbackRejected, "error.code", "fallback_RPC_evidence", fmt.Sprintf("observed=%d expected=-32601_or_-32022", response.Error.Code))
	}
	if err := validateUnsupportedVersion(response.Error.Data, contract.ModernProtocolVersion); err != nil {
		return false, false, err
	}
	attempt = 2
	rpcCode = 0
	requestID, wire, err = coordinator.rawRequest(ctx, "server/discover", params, RequestOptions{ProtocolVersion: contract.ModernProtocolVersion})
	if err != nil {
		return false, false, err
	}
	if wire.OAuthChallenge != nil {
		return false, false, wire.OAuthChallenge.at(OAuthChallengeModernDiscovery)
	}
	if len(wire.SessionIDs) != 0 {
		return false, false, negotiationPredicate(ErrSessionLost, "session_header", "modern_stateless", fmt.Sprintf("observed=%d expected=0", len(wire.SessionIDs)))
	}
	response, err = decodeNegotiationResponse(requestID, wire)
	if response.Error != nil {
		rpcCode = response.Error.Code
	}
	if err != nil {
		return false, false, diagnostics.WithDetail(ErrUnsupportedProtocol, diagnostics.Snapshot("downstream", "retry discovery", "", err))
	}
	if response.Error != nil {
		return false, false, negotiationPredicate(ErrUnsupportedProtocol, "error", "retry_requires_result", fmt.Sprintf("RPC_code=%d", rpcCode))
	}
	if err := validateDiscoverResult(response.Result); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func negotiateLegacy(ctx context.Context, coordinator *Coordinator) (result *Runtime, resultErr error) {
	var wire WireResponse
	var rpcCode int64
	method := "initialize"
	defer func() {
		resultErr = negotiationContext(resultErr, method, contract.LegacyProtocolVersion, 1, wire, rpcCode)
	}()
	params, _ := json.Marshal(initializeParams{ProtocolVersion: contract.LegacyProtocolVersion, ClientInfo: downstreamClientInfo()})
	requestID, wire, err := coordinator.rawRequest(ctx, "initialize", params, RequestOptions{ProtocolVersion: contract.LegacyProtocolVersion})
	if err != nil {
		return nil, errors.Join(err, coordinator.Close(ctx))
	}
	if wire.OAuthChallenge != nil {
		return nil, wire.OAuthChallenge.at(OAuthChallengeLegacyInitialize)
	}
	response, err := decodeNegotiationResponse(requestID, wire)
	if response.Error != nil {
		rpcCode = response.Error.Code
	}
	if err != nil || response.Error != nil {
		if err == nil {
			err = negotiationPredicate(ErrUnsupportedProtocol, "error", "initialize_requires_result", fmt.Sprintf("RPC_code=%d", rpcCode))
		}
		return nil, errors.Join(diagnostics.WithDetail(ErrUnsupportedProtocol, diagnostics.Snapshot("downstream", "initialize", "", err)), coordinator.Close(ctx))
	}
	if err := validateInitializeResult(response.Result); err != nil {
		return nil, errors.Join(err, coordinator.Close(ctx))
	}
	sessionID, err := initialSession(wire.SessionIDs)
	if err != nil {
		return nil, errors.Join(err, coordinator.Close(ctx))
	}
	method = "notifications/initialized"
	notification, err := coordinator.Notify(ctx, "notifications/initialized", json.RawMessage(`{}`), RequestOptions{ProtocolVersion: contract.LegacyProtocolVersion, SessionID: sessionID})
	wire = notification
	if err != nil || !successfulNotification(notification) || !sameSession(sessionID, notification.SessionIDs) {
		if err == nil {
			err = negotiationPredicate(ErrSessionLost, "initialized", "notification_status_and_session", fmt.Sprintf("status_success=%t session_matches=%t", successfulNotification(notification), sameSession(sessionID, notification.SessionIDs)))
		}
		return nil, errors.Join(err, coordinator.Close(ctx))
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
	if wire.OAuthChallenge != nil && method == "tools/list" {
		return Response{}, wire.OAuthChallenge.at(OAuthChallengeCatalogFirstPage)
	}
	if wire.StatusCode == http.StatusUnauthorized || wire.StatusCode == http.StatusForbidden {
		return Response{}, negotiationContext(ErrAuthenticationRejected, method, version, 1, wire, 0)
	}
	if wire.StatusCode == http.StatusRequestTimeout || wire.StatusCode == http.StatusTooEarly || wire.StatusCode == http.StatusTooManyRequests || wire.StatusCode >= http.StatusInternalServerError {
		return Response{}, negotiationContext(ErrRemoteUnavailable, method, version, 1, wire, 0)
	}
	if !runtimeSessionCurrent(era, sessionID, wire) {
		_ = runtime.Close(ctx)
		return Response{}, ErrSessionLost
	}
	return decodeNegotiationResponse(requestID, wire)
}

func (runtime *Runtime) Close(ctx context.Context) error {
	runtime.closeMu.Lock()
	defer runtime.closeMu.Unlock()
	runtime.mu.Lock()
	if runtime.closeDone {
		err := runtime.closeErr
		runtime.mu.Unlock()
		return err
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
	err := coordinator.Close(ctx)
	runtime.mu.Lock()
	runtime.closeDone = true
	runtime.closeErr = err
	runtime.mu.Unlock()
	return err
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
			return Response{}, negotiationPredicate(ErrFallbackRejected, "HTTP_status", "successful_response", fmt.Sprintf("observed=%d expected=200..299", wire.StatusCode))
		}
		mediaType, _, err := mime.ParseMediaType(wire.ContentType)
		if err != nil || mediaType != contract.MediaTypeJSON && mediaType != contract.MediaTypeEventStream {
			return Response{}, negotiationPredicate(ErrFallbackRejected, "Content-Type", "response_media", "observed="+negotiationMedia(wire.ContentType)+" expected=json_or_event_stream")
		}
	}
	response, err := decodeResponse(requestID, wire.Body)
	if err != nil {
		err = negotiationPredicate(err, "response_envelope", "JSON_RPC_contract", fmt.Sprintf("expected=bounded_unique_JSON_RPC_matching_ID observed_bytes=%d allowed_bytes=%d", len(wire.Body), limit("downstream_mcp_body_bytes")))
	}
	return response, err
}

func validateDiscoverResult(raw json.RawMessage) error {
	var result discoverResult
	if err := strictjson.Decode(raw, &result, strictjson.Options{MaxBytes: limit("downstream_mcp_body_bytes"), MaxDepth: int(limit("json_depth")), RejectUnknownMembers: true}); err != nil {
		return negotiationShape(raw, map[string]string{"resultType": "string", "_meta": "object", "ttlMs": "integer", "cacheScope": "string", "supportedVersions": "array", "capabilities": "any", "instructions": "string"})
	}
	if result.ResultType != "" && result.ResultType != "complete" {
		return negotiationPredicate(ErrUnsupportedProtocol, "resultType", "enum", "expected=absent_or_complete observed=other_string")
	}
	if result.TTLMs == nil {
		return negotiationPredicate(ErrUnsupportedProtocol, "ttlMs", "required", "observed=missing_or_null expected=nonnegative_integer")
	}
	if *result.TTLMs < 0 {
		return negotiationPredicate(ErrUnsupportedProtocol, "ttlMs", "minimum", fmt.Sprintf("observed=%d expected_minimum=0", *result.TTLMs))
	}
	if result.CacheScope == nil {
		return negotiationPredicate(ErrUnsupportedProtocol, "cacheScope", "required", "observed=missing_or_null")
	}
	if *result.CacheScope != "public" && *result.CacheScope != "private" {
		return negotiationPredicate(ErrUnsupportedProtocol, "cacheScope", "enum", "observed=other_string expected=public_or_private")
	}
	if result.SupportedVersions == nil {
		return negotiationPredicate(ErrUnsupportedProtocol, "supportedVersions", "required", "observed=missing_or_null")
	}
	if !containsExactVersion(*result.SupportedVersions, contract.ModernProtocolVersion) {
		return negotiationPredicate(ErrUnsupportedProtocol, "supportedVersions", "unique_nonempty_exact_version", fmt.Sprintf("count=%d required_version=%s", len(*result.SupportedVersions), contract.ModernProtocolVersion))
	}
	if !jsonObject(result.Capabilities) {
		return negotiationPredicate(ErrUnsupportedProtocol, "capabilities", "kind", "expected=object observed=missing_or_nonobject")
	}
	return nil
}

func validateUnsupportedVersion(raw json.RawMessage, requested string) error {
	var data unsupportedVersionData
	if err := strictjson.Decode(raw, &data, strictjson.Options{MaxBytes: limit("downstream_mcp_body_bytes"), MaxDepth: int(limit("json_depth")), RejectUnknownMembers: true}); err != nil {
		return negotiationShape(raw, map[string]string{"supported": "array", "requested": "string"})
	}
	if data.Requested != requested {
		return negotiationPredicate(ErrUnsupportedProtocol, "error.data.requested", "exact_request_version", "matches=false")
	}
	if !containsExactVersion(data.Supported, contract.ModernProtocolVersion) {
		return negotiationPredicate(ErrUnsupportedProtocol, "error.data.supported", "unique_nonempty_exact_version", fmt.Sprintf("count=%d required_version=%s", len(data.Supported), contract.ModernProtocolVersion))
	}
	return nil
}

func validateInitializeResult(raw json.RawMessage) error {
	var result initializeResult
	if err := strictjson.Decode(raw, &result, strictjson.Options{MaxBytes: limit("downstream_mcp_body_bytes"), MaxDepth: int(limit("json_depth")), RejectUnknownMembers: true}); err != nil {
		return negotiationShape(raw, map[string]string{"_meta": "object", "capabilities": "any", "instructions": "string", "protocolVersion": "string", "serverInfo": "any"})
	}
	if result.ProtocolVersion != contract.LegacyProtocolVersion {
		return negotiationPredicate(ErrUnsupportedProtocol, "protocolVersion", "exact_legacy_version", "matches=false expected="+contract.LegacyProtocolVersion)
	}
	if !jsonObject(result.Capabilities) {
		return negotiationPredicate(ErrUnsupportedProtocol, "capabilities", "kind", "expected=object observed=missing_or_nonobject")
	}
	if !validServerImplementation(result.ServerInfo) {
		return negotiationPredicate(ErrUnsupportedProtocol, "serverInfo", "implementation_contract", "expected=closed_object_with_nonempty_name_and_version")
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

func isLegacyVersionFallback(wire WireResponse) bool {
	if wire.StatusCode != http.StatusBadRequest {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(wire.ContentType)
	if err != nil || mediaType != contract.MediaTypeJSON {
		return false
	}
	var envelope legacyVersionError
	if err := strictjson.Decode(wire.Body, &envelope, strictjson.Options{MaxBytes: limit("downstream_mcp_body_bytes"), MaxDepth: int(limit("json_depth")), RejectUnknownMembers: true}); err != nil || envelope.JSONRPC != "2.0" || envelope.ID != "server-error" || envelope.Error.Code != -32600 || !nullOrAbsent(envelope.Error.Data) {
		return false
	}
	prefix := "Bad Request: Unsupported protocol version: " + contract.ModernProtocolVersion + ". Supported versions: "
	if !strings.HasPrefix(envelope.Error.Message, prefix) {
		return false
	}
	allowed := map[string]struct{}{
		"2024-11-05":                   {},
		"2025-03-26":                   {},
		"2025-06-18":                   {},
		contract.LegacyProtocolVersion: {},
	}
	seen := make(map[string]struct{})
	for _, version := range strings.Split(strings.TrimPrefix(envelope.Error.Message, prefix), ", ") {
		if _, ok := allowed[version]; !ok {
			return false
		}
		if _, duplicate := seen[version]; duplicate {
			return false
		}
		seen[version] = struct{}{}
	}
	_, supported := seen[contract.LegacyProtocolVersion]
	return supported
}

func nullOrAbsent(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}

func initialSession(values []string) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 || values[0] == "" || int64(len(values[0])) > limit("downstream_legacy_session_id_bytes") {
		return "", negotiationPredicate(ErrSessionLost, "session_header", "legacy_initial_session", fmt.Sprintf("count=%d expected_count=1 allowed_bytes=%d value=withheld", len(values), limit("downstream_legacy_session_id_bytes")))
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
