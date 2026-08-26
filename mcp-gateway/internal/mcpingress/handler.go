package mcpingress

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var ErrAuthenticationRequired = errors.New("agent authentication required")

type AgentAuthenticator interface {
	Authenticate(context.Context, string) (*authorization.Lease, error)
}

type DenyAllAuthenticator struct{}

func (DenyAllAuthenticator) Authenticate(context.Context, string) (*authorization.Lease, error) {
	return nil, ErrAuthenticationRequired
}

type Timer interface {
	Stop() bool
	Reset(time.Duration) bool
}

type Options struct {
	Authenticator AgentAuthenticator
	Now           func() time.Time
	Entropy       io.Reader
	AfterFunc     func(time.Duration, func()) Timer
	Next          http.Handler
}

type Handler struct {
	authenticator AgentAuthenticator
	now           func() time.Time
	entropy       io.Reader
	afterFunc     func(time.Duration, func()) Timer
	next          http.Handler
	modern        http.Handler
	work          chan struct{}
	streams       chan struct{}
	mu            sync.Mutex
	legacy        map[string]*legacySession
	reserved      int
	shuttingDown  bool
}

type legacySession struct {
	lease         *authorization.Lease
	binding       authorization.CredentialBinding
	createdAt     time.Time
	lastActive    time.Time
	handler       http.Handler
	done          chan struct{}
	idleTimer     Timer
	absoluteTimer Timer
	closeOnce     sync.Once
}

type leaseContextKey struct{}

type requestLease struct {
	mu          sync.Mutex
	lease       *authorization.Lease
	transferred bool
	released    bool
}

func (ownership *requestLease) Release() {
	ownership.mu.Lock()
	if ownership.released || ownership.transferred {
		ownership.mu.Unlock()
		return
	}
	ownership.released = true
	lease := ownership.lease
	ownership.mu.Unlock()
	lease.Release()
}

func (ownership *requestLease) transfer() bool {
	ownership.mu.Lock()
	defer ownership.mu.Unlock()
	if ownership.released || ownership.transferred {
		return false
	}
	ownership.transferred = true
	return true
}

func New(options Options) *Handler {
	if options.Authenticator == nil {
		options.Authenticator = DenyAllAuthenticator{}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Entropy == nil {
		options.Entropy = rand.Reader
	}
	if options.AfterFunc == nil {
		options.AfterFunc = func(duration time.Duration, callback func()) Timer {
			return time.AfterFunc(duration, callback)
		}
	}
	if options.Next == nil {
		options.Next = http.NotFoundHandler()
	}
	server := mcp.NewServer(
		&mcp.Implementation{Name: "mcp-gateway", Version: "s1"},
		&mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{}},
	)
	modern := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless:                    true,
			JSONResponse:                 true,
			MaxRequestBodyBytes:          int64(limit("mcp_body_bytes")),
			PropagateRequestCancellation: true,
		},
	)
	return &Handler{
		authenticator: options.Authenticator,
		now:           options.Now,
		entropy:       options.Entropy,
		afterFunc:     options.AfterFunc,
		next:          options.Next,
		modern:        modern,
		work:          make(chan struct{}, limit("mcp_work")),
		streams:       make(chan struct{}, limit("mcp_streams")),
		legacy:        make(map[string]*legacySession),
	}
}

func (handler *Handler) Authenticate(ctx context.Context, request *http.Request, authority contract.CredentialAuthority) (context.Context, error) {
	if authority != contract.AuthorityAgent {
		return ctx, httpboundary.Error{Code: contract.ProblemAuthenticationRequired}
	}
	bearer, code := agentBearer(request.Header.Values("Authorization"))
	if code != "" {
		return ctx, httpboundary.Error{Code: code}
	}
	lease, err := handler.authenticator.Authenticate(ctx, bearer)
	if err != nil || lease == nil {
		if lease != nil {
			lease.Release()
		}
		return ctx, httpboundary.Error{Code: contract.ProblemAuthenticationRequired}
	}
	binding := lease.Binding()
	if !lease.Current() || binding.PrincipalID == "" || binding.PrincipalRevision == "" || binding.CredentialID == "" ||
		binding.CredentialRevision == "" || binding.CredentialFingerprint == "" || binding.Visibility == "" {
		lease.Release()
		return ctx, httpboundary.Error{Code: contract.ProblemAuthenticationRequired}
	}
	ownership := &requestLease{lease: lease}
	authenticated := context.WithValue(ctx, leaseContextKey{}, ownership)
	return httpboundary.WithAuthenticationCleanup(authenticated, ownership), nil
}

func LeaseFromContext(ctx context.Context) (*authorization.Lease, bool) {
	ownership, ok := requestLeaseFromContext(ctx)
	if !ok {
		return nil, false
	}
	return ownership.lease, true
}

func requestLeaseFromContext(ctx context.Context) (*requestLease, bool) {
	ownership, ok := ctx.Value(leaseContextKey{}).(*requestLease)
	return ownership, ok && ownership != nil && ownership.lease != nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/mcp" {
		handler.next.ServeHTTP(writer, request)
		return
	}
	ownership, ok := requestLeaseFromContext(request.Context())
	if !ok {
		writeProblem(writer, contract.ProblemAuthenticationRequired)
		return
	}
	defer ownership.Release()
	lease := ownership.lease
	request, stopWatchingLease := requestWithLeaseCancellation(request, lease)
	defer stopWatchingLease()
	if !lease.Current() {
		writeProblem(writer, contract.ProblemAuthenticationRequired)
		return
	}
	binding := lease.Binding()
	if !tryAcquire(handler.work) {
		writeProblem(writer, contract.ProblemResourceLimit)
		return
	}
	defer release(handler.work)

	if request.Method == http.MethodGet || request.Method == http.MethodDelete {
		if request.Header.Get("Mcp-Protocol-Version") == contract.ModernProtocolVersion {
			writer.Header().Set("Allow", http.MethodPost)
			writeProblem(writer, contract.ProblemMethodNotAllowed)
			return
		}
		if request.Header.Get("Mcp-Protocol-Version") != contract.LegacyProtocolVersion || request.Header.Get("Mcp-Session-Id") == "" {
			writeProblem(writer, contract.ProblemMalformedRequest)
			return
		}
		if body, code := readMCPBody(request); code != "" || len(body) != 0 {
			if code == "" {
				code = contract.ProblemMalformedRequest
			}
			writeProblem(writer, code)
			return
		}
		handler.serveLegacyExisting(writer, request, binding)
		return
	}
	if request.Method != http.MethodPost {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	if mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type")); err != nil || mediaType != contract.MediaTypeJSON {
		writeProblem(writer, contract.ProblemUnsupportedMediaType)
		return
	}
	body, code := readMCPBody(request)
	if code != "" {
		writeProblem(writer, code)
		return
	}
	wire, era, code := classifyPOST(request, body)
	if code != "" {
		writeProblem(writer, code)
		return
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	switch era {
	case eraModern:
		if wire.Method == "subscriptions/listen" {
			if !tryAcquire(handler.streams) {
				writeProblem(writer, contract.ProblemResourceLimit)
				return
			}
			defer release(handler.streams)
		}
		request.Header.Set("Mcp-Method", wire.Method)
		handler.modern.ServeHTTP(writer, request)
	case eraLegacyInitialize:
		handler.serveLegacyInitialize(writer, request, ownership)
	case eraLegacyExisting:
		handler.serveLegacyExisting(writer, request, binding)
	default:
		writeProblem(writer, contract.ProblemMalformedRequest)
	}
}

func requestWithLeaseCancellation(request *http.Request, lease *authorization.Lease) (*http.Request, func()) {
	ctx, cancel := context.WithCancel(request.Context())
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-lease.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return request.WithContext(ctx), func() {
		cancel()
		<-watcherDone
	}
}

func (handler *Handler) Status() (work, streams, legacy contract.LimitStatus) {
	handler.mu.Lock()
	legacyInUse := int64(len(handler.legacy) + handler.reserved)
	handler.mu.Unlock()
	return permitStatus(handler.work), permitStatus(handler.streams), fixedStatus("legacy_sessions", legacyInUse)
}

func (handler *Handler) Shutdown() {
	handler.mu.Lock()
	handler.shuttingDown = true
	ids := make([]string, 0, len(handler.legacy))
	for id := range handler.legacy {
		ids = append(ids, id)
	}
	handler.mu.Unlock()
	for _, id := range ids {
		handler.closeLegacy(id)
	}
}

type requestEra uint8

const (
	eraInvalid requestEra = iota
	eraModern
	eraLegacyInitialize
	eraLegacyExisting
)

type wireRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func classifyPOST(request *http.Request, body []byte) (wireRequest, requestEra, contract.ProblemCode) {
	var wire wireRequest
	if err := json.Unmarshal(body, &wire); err != nil || wire.JSONRPC != "2.0" || wire.Method == "" || len(wire.Params) == 0 {
		return wireRequest{}, eraInvalid, contract.ProblemMalformedRequest
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(wire.Params, &params); err != nil {
		return wireRequest{}, eraInvalid, contract.ProblemMalformedRequest
	}
	var initializeVersion string
	if value := params["protocolVersion"]; value != nil {
		if err := json.Unmarshal(value, &initializeVersion); err != nil {
			return wireRequest{}, eraInvalid, contract.ProblemMalformedRequest
		}
	}
	var metadata map[string]json.RawMessage
	if value := params["_meta"]; value != nil {
		if err := json.Unmarshal(value, &metadata); err != nil {
			return wireRequest{}, eraInvalid, contract.ProblemMalformedRequest
		}
	}
	var metadataVersion string
	if value := metadata[mcp.MetaKeyProtocolVersion]; value != nil {
		if err := json.Unmarshal(value, &metadataVersion); err != nil {
			return wireRequest{}, eraInvalid, contract.ProblemMalformedRequest
		}
	}
	headerVersion := request.Header.Get("Mcp-Protocol-Version")
	sessionID := request.Header.Get("Mcp-Session-Id")
	claimsModern := headerVersion == contract.ModernProtocolVersion || metadataVersion == contract.ModernProtocolVersion || initializeVersion == contract.ModernProtocolVersion
	if claimsModern {
		if headerVersion != contract.ModernProtocolVersion || metadataVersion != contract.ModernProtocolVersion || sessionID != "" || wire.Method == "initialize" {
			return wireRequest{}, eraInvalid, contract.ProblemMalformedRequest
		}
		if method := request.Header.Get("Mcp-Method"); method != "" && method != wire.Method {
			return wireRequest{}, eraInvalid, contract.ProblemMalformedRequest
		}
		return wire, eraModern, ""
	}
	if wire.Method == "initialize" && initializeVersion == contract.LegacyProtocolVersion && sessionID == "" && (headerVersion == "" || headerVersion == contract.LegacyProtocolVersion) {
		return wire, eraLegacyInitialize, ""
	}
	if wire.Method != "initialize" && sessionID != "" && headerVersion == contract.LegacyProtocolVersion {
		return wire, eraLegacyExisting, ""
	}
	return wireRequest{}, eraInvalid, contract.ProblemMalformedRequest
}

func (handler *Handler) serveLegacyInitialize(writer http.ResponseWriter, request *http.Request, ownership *requestLease) {
	if !handler.reserveLegacy() {
		writeProblem(writer, contract.ProblemResourceLimit)
		return
	}
	reserved := true
	defer func() {
		if reserved {
			handler.releaseLegacyReservation()
		}
	}()

	lease := ownership.lease
	if !lease.Current() {
		writeProblem(writer, contract.ProblemAuthenticationRequired)
		return
	}
	secret := make([]byte, 32)
	if _, err := io.ReadFull(handler.entropy, secret); err != nil {
		writeProblem(writer, contract.ProblemStorageUnavailable)
		return
	}
	sessionID := base64.RawURLEncoding.EncodeToString(secret)
	now := handler.now()
	session := &legacySession{
		lease: lease, binding: lease.Binding(), createdAt: now, lastActive: now,
		handler: newLegacySDK(sessionID), done: make(chan struct{}),
	}
	session.idleTimer = handler.afterFunc(contract.LegacyIdleLifetime, func() { handler.closeLegacyCandidate(sessionID, session) })
	session.absoluteTimer = handler.afterFunc(contract.LegacyAbsoluteLifetime, func() { handler.closeLegacyCandidate(sessionID, session) })
	if !lease.Current() {
		if session.idleTimer != nil {
			session.idleTimer.Stop()
		}
		if session.absoluteTimer != nil {
			session.absoluteTimer.Stop()
		}
		finishLegacySession(sessionID, session, false)
		writeProblem(writer, contract.ProblemAuthenticationRequired)
		return
	}

	handler.mu.Lock()
	if handler.shuttingDown || handler.legacy[sessionID] != nil {
		handler.mu.Unlock()
		finishLegacySession(sessionID, session, false)
		writeProblem(writer, contract.ProblemResourceLimit)
		return
	}
	if !lease.Current() {
		handler.mu.Unlock()
		finishLegacySession(sessionID, session, false)
		writeProblem(writer, contract.ProblemAuthenticationRequired)
		return
	}
	handler.reserved--
	reserved = false
	handler.legacy[sessionID] = session
	if !ownership.transfer() {
		delete(handler.legacy, sessionID)
		handler.mu.Unlock()
		finishLegacySession(sessionID, session, false)
		writeProblem(writer, contract.ProblemAuthenticationRequired)
		return
	}
	handler.mu.Unlock()

	go func() {
		select {
		case <-session.lease.Done():
			handler.closeLegacyCandidate(sessionID, session)
		case <-session.done:
		}
	}()
	if !session.lease.Current() {
		handler.closeLegacyCandidate(sessionID, session)
		writeProblem(writer, contract.ProblemAuthenticationRequired)
		return
	}
	captured := &statusWriter{ResponseWriter: writer}
	session.handler.ServeHTTP(captured, request)
	if captured.status >= http.StatusBadRequest || !session.lease.Current() {
		handler.closeLegacyCandidate(sessionID, session)
	}
}

func (handler *Handler) serveLegacyExisting(writer http.ResponseWriter, request *http.Request, binding authorization.CredentialBinding) {
	sessionID := request.Header.Get("Mcp-Session-Id")
	now := handler.now()
	handler.mu.Lock()
	session := handler.legacy[sessionID]
	if session == nil || session.binding.PrincipalID != binding.PrincipalID || session.binding.CredentialID != binding.CredentialID ||
		session.binding.CredentialRevision != binding.CredentialRevision {
		handler.mu.Unlock()
		writeProblem(writer, contract.ProblemNotFound)
		return
	}
	expired := !now.Before(session.createdAt.Add(contract.LegacyAbsoluteLifetime)) || !now.Before(session.lastActive.Add(contract.LegacyIdleLifetime))
	if expired || !session.lease.Current() {
		delete(handler.legacy, sessionID)
		handler.mu.Unlock()
		closeLegacySession(sessionID, session)
		writeProblem(writer, contract.ProblemNotFound)
		return
	}
	session.lastActive = now
	if session.idleTimer != nil {
		session.idleTimer.Reset(contract.LegacyIdleLifetime)
	}
	if request.Method == http.MethodDelete {
		delete(handler.legacy, sessionID)
	}
	handler.mu.Unlock()

	if request.Method == http.MethodGet {
		if !tryAcquire(handler.streams) {
			writeProblem(writer, contract.ProblemResourceLimit)
			return
		}
		defer release(handler.streams)
	}
	if request.Method == http.MethodDelete {
		defer finishLegacySession(sessionID, session, false)
	}
	session.handler.ServeHTTP(writer, request)
}

func (handler *Handler) reserveLegacy() bool {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.shuttingDown || len(handler.legacy)+handler.reserved >= limit("legacy_sessions") {
		return false
	}
	handler.reserved++
	return true
}

func (handler *Handler) releaseLegacyReservation() {
	handler.mu.Lock()
	handler.reserved--
	handler.mu.Unlock()
}

func (handler *Handler) closeLegacy(sessionID string) {
	handler.mu.Lock()
	session := handler.legacy[sessionID]
	handler.mu.Unlock()
	if session != nil {
		handler.closeLegacyCandidate(sessionID, session)
	}
}

func (handler *Handler) closeLegacyCandidate(sessionID string, session *legacySession) {
	handler.mu.Lock()
	if handler.legacy[sessionID] == session {
		delete(handler.legacy, sessionID)
	}
	handler.mu.Unlock()
	closeLegacySession(sessionID, session)
}

func closeLegacySession(sessionID string, session *legacySession) {
	finishLegacySession(sessionID, session, true)
}

func finishLegacySession(sessionID string, session *legacySession, terminateSDK bool) {
	session.closeOnce.Do(func() {
		if session.idleTimer != nil {
			session.idleTimer.Stop()
		}
		if session.absoluteTimer != nil {
			session.absoluteTimer.Stop()
		}
		close(session.done)
		session.lease.Release()
		if terminateSDK {
			request, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, "http://127.0.0.1/mcp", nil)
			if err == nil {
				request.Header.Set("Mcp-Session-Id", sessionID)
				request.Header.Set("Mcp-Protocol-Version", contract.LegacyProtocolVersion)
				session.handler.ServeHTTP(newDiscardWriter(), request)
			}
		}
	})
}

func newLegacySDK(sessionID string) http.Handler {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "mcp-gateway", Version: "s1"},
		&mcp.ServerOptions{
			Capabilities: &mcp.ServerCapabilities{},
			GetSessionID: func() string { return sessionID },
		},
	)
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			JSONResponse:        true,
			MaxRequestBodyBytes: int64(limit("mcp_body_bytes")),
		},
	)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (writer *statusWriter) WriteHeader(status int) {
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusWriter) Write(body []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return writer.ResponseWriter.Write(body)
}

type discardWriter struct {
	header http.Header
}

func newDiscardWriter() *discardWriter                { return &discardWriter{header: make(http.Header)} }
func (writer *discardWriter) Header() http.Header     { return writer.header }
func (*discardWriter) WriteHeader(int)                {}
func (*discardWriter) Write(body []byte) (int, error) { return len(body), nil }

func readMCPBody(request *http.Request) ([]byte, contract.ProblemCode) {
	maximum := int64(limit("mcp_body_bytes"))
	body, err := io.ReadAll(io.LimitReader(request.Body, maximum+1))
	if err != nil {
		return nil, contract.ProblemMalformedRequest
	}
	if int64(len(body)) > maximum {
		return nil, contract.ProblemBodyTooLarge
	}
	return body, ""
}

func agentBearer(values []string) (string, contract.ProblemCode) {
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", contract.ProblemAuthenticationRequired
	}
	bearer := strings.TrimPrefix(values[0], "Bearer ")
	if bearer == "" || strings.ContainsAny(bearer, " \t\r\n") {
		return "", contract.ProblemAuthenticationRequired
	}
	if strings.HasPrefix(bearer, contract.AdminBearerPrefix) {
		return "", contract.ProblemCredentialDomainMismatch
	}
	if !strings.HasPrefix(bearer, contract.AgentBearerPrefix) {
		return "", contract.ProblemAuthenticationRequired
	}
	return bearer, ""
}

func tryAcquire(permit chan struct{}) bool {
	select {
	case permit <- struct{}{}:
		return true
	default:
		return false
	}
}

func release(permit chan struct{}) { <-permit }

func permitStatus(permit chan struct{}) contract.LimitStatus {
	inUse, maximum := int64(len(permit)), int64(cap(permit))
	return contract.LimitStatus{InUse: inUse, Limit: maximum, Saturated: inUse >= maximum}
}

func fixedStatus(name string, inUse int64) contract.LimitStatus {
	return contract.LimitStatus{InUse: inUse, Limit: int64(limit(name)), Saturated: inUse >= int64(limit(name))}
}

func limit(name string) int {
	value, ok := contract.FixedLimitByName(name)
	if !ok {
		panic("missing contract limit: " + name)
	}
	return int(value.Maximum)
}

func writeProblem(writer http.ResponseWriter, code contract.ProblemCode) {
	problem, _ := contract.ProblemForCode(code)
	if code == contract.ProblemAuthenticationRequired {
		writer.Header().Set("WWW-Authenticate", "Bearer")
	}
	writer.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
	writer.WriteHeader(problem.Status)
	_ = json.NewEncoder(writer).Encode(contract.ProblemEnvelope(problem))
}
