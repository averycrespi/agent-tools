// Package httpproxy implements the receipt-gated HTTP proxy engine.
package httpproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
)

var ErrUnavailable = errors.New("HTTP proxy unavailable")

type Options struct {
	Authority    *authorization.Repository
	Evidence     *invocation.Repository
	Admissions   *invocation.AdmissionCoordinator
	Materials    *httpcredentials.Service
	GitMaterials *gitcredentials.Service
	Remote       *remote.Factory
	Signer       *httpca.Signer
	Listeners    func() []netip.AddrPort
	Now          func() time.Time
	Ready        func() bool
	Diagnostics  diagnostics.HTTPProxyObserver
	Observations *diagnostics.Observations
}

type Engine struct {
	options     Options
	mu          sync.Mutex
	draining    bool
	connections map[net.Conn]struct{}
	work        int
	streams     int
	tunnels     int
	principals  map[string]int
	server      *http.Server
	// Fixture-only trust/timer seams are not public configuration.
	roots     *x509.CertPool
	afterFunc func(time.Duration, func()) func()
}

func New(options Options) (*Engine, error) {
	if options.Authority == nil || options.Evidence == nil || options.Admissions == nil || options.Materials == nil || options.Remote == nil || options.Listeners == nil || options.Now == nil {
		return nil, ErrUnavailable
	}
	if options.Observations == nil {
		options.Observations = diagnostics.NewObservations()
	}
	e := &Engine{options: options, connections: make(map[net.Conn]struct{}), principals: make(map[string]int), afterFunc: func(d time.Duration, f func()) func() { t := time.AfterFunc(d, f); return func() { t.Stop() } }}
	e.server = e.newServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { e.handle(w, r, nil) }))
	return e, nil
}

func (e *Engine) newServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: contract.HTTPProxyHeaderTimeout, IdleTimeout: contract.HTTPProxyIdleTimeout, MaxHeaderBytes: contract.HTTPProxyHeaderBytes, ErrorLog: diagnostics.HTTPErrorLog(e.options.Diagnostics)}
}

// Serve consumes an already bound composition-owned listener; New never binds a socket.
func (e *Engine) Serve(listener net.Listener) error {
	return e.server.Serve(&boundListener{Listener: listener, engine: e})
}

func (e *Engine) Close(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, contract.HTTPProxyDrainTimeout)
	defer cancel()
	e.BeginDrain()
	return e.Wait(ctx)
}

func (e *Engine) BeginDrain() {
	e.mu.Lock()
	e.draining = true
	connections := make([]net.Conn, 0, len(e.connections))
	for c := range e.connections {
		connections = append(connections, c)
	}
	e.mu.Unlock()
	_ = e.server.Close()
	for _, c := range connections {
		_ = c.Close()
	}
}

// Wait never relinquishes actual work ownership on a caller's timeout.
func (e *Engine) Wait(ctx context.Context) error {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		e.mu.Lock()
		done := e.work == 0 && len(e.connections) == 0
		e.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (e *Engine) acquire(principal string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.draining {
		return false
	}
	if principal == "" {
		if e.work >= contract.HTTPProxyWork {
			return false
		}
		e.work++
		return true
	}
	if e.principals[principal] >= contract.HTTPProxyPrincipalWork {
		return false
	}
	e.principals[principal]++
	return true
}
func (e *Engine) release(principal string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if principal == "" {
		e.work--
	} else {
		e.principals[principal]--
		if e.principals[principal] == 0 {
			delete(e.principals, principal)
		}
	}
}

type intercepted struct {
	destination httppolicy.Destination
	bearer      string
	binding     authorization.CredentialBinding
	sni         string
	connect     contract.HTTPConnectContext
}

func admissionContext(inside *intercepted) authorization.HTTPAdmissionContext {
	if inside == nil || inside.connect.ID == "" {
		return authorization.HTTPAdmissionContext{}
	}
	copy := inside.connect
	return authorization.HTTPAdmissionContext{Connect: &copy}
}

func (e *Engine) handle(w http.ResponseWriter, r *http.Request, inside *intercepted) {
	started := time.Now()
	protocol := diagnostics.HTTP
	if r.Method == http.MethodConnect {
		protocol = diagnostics.Connect
	}
	e.options.Observations.Request(protocol)
	defer func() { e.options.Observations.Latency(protocol, diagnostics.RequestStage, time.Since(started)) }()
	tracked := &failureWriter{ResponseWriter: w, id: proxyID(), resource: diagnostics.Text(r.Host, 160), secrets: diagnostics.HTTPSecrets(r.Header)}
	w = tracked
	// Never allow net/http's default panic logger to receive request material.
	defer func() {
		if recovered := recover(); recovered != nil {
			recoveredErr, _ := recovered.(error)
			if !errors.Is(recoveredErr, http.ErrAbortHandler) {
				detail := diagnostics.Panic("proxy", "handle", r.Host, recovered, tracked.secrets...)
				e.observeFailure(w, diagnostics.ProxyPanic, diagnostics.WithDetail(ErrUnavailable, detail))
				if !tracked.started {
					reject(w, http.StatusServiceUnavailable)
					return
				}
			}
			panic(http.ErrAbortHandler)
		}
	}()
	if e.options.Ready != nil && !e.options.Ready() {
		reject(w, http.StatusServiceUnavailable)
		return
	}
	if !e.acquire("") {
		e.observeProxy(started, diagnostics.HTTPProxyFailure, diagnostics.ProxyCapacity, diagnostics.Capacity, tracked.id)
		rejectCapacity(w, http.StatusServiceUnavailable)
		return
	}
	defer e.release("")
	bearer := ""
	if inside == nil {
		var valid bool
		bearer, valid = proxyBearer(r.Header.Values("Proxy-Authorization"))
		if !valid {
			reject(w, http.StatusProxyAuthRequired)
			return
		}
	} else {
		bearer = inside.bearer
	}
	lease, err := e.options.Authority.Authenticate(r.Context(), bearer)
	if err != nil {
		status := http.StatusServiceUnavailable
		switch {
		case errors.Is(err, authorization.ErrResourceLimit):
			status = http.StatusTooManyRequests
		case errors.Is(err, authorization.ErrAuthenticationRequired), errors.Is(err, authorization.ErrCredentialDomainMismatch):
			status = http.StatusProxyAuthRequired
		}
		reject(w, status)
		return
	}
	defer lease.Release()
	binding := lease.Binding()
	if inside != nil && (binding.PrincipalID != inside.binding.PrincipalID || binding.CredentialID != inside.binding.CredentialID || binding.CredentialRevision != inside.binding.CredentialRevision) {
		reject(w, http.StatusProxyAuthRequired)
		return
	}
	if !e.acquire(binding.PrincipalID) {
		e.observeProxy(started, diagnostics.HTTPProxyFailure, diagnostics.ProxyCapacity, diagnostics.Capacity, tracked.id)
		rejectCapacity(w, http.StatusTooManyRequests)
		return
	}
	defer e.release(binding.PrincipalID)
	if inside != nil && len(r.Header.Values("Proxy-Authorization")) != 0 {
		e.rejectInvalid(w, r, lease, inside, "headers", "inner_proxy_authorization")
		return
	}
	if remote.ValidateProxyHeaders(r.Header) != nil {
		e.rejectInvalid(w, r, lease, inside, "headers", "invalid_headers")
		return
	}
	if len(r.Trailer) != 0 {
		e.rejectInvalid(w, r, lease, inside, "headers", "trailers_unsupported")
		return
	}
	if r.Header.Get("Upgrade") != "" || hasConnectionToken(r.Header, "upgrade") {
		e.rejectInvalid(w, r, lease, inside, "headers", "upgrade_unsupported")
		return
	}
	if r.Method == http.MethodConnect {
		if inside != nil {
			e.rejectInvalid(w, r, lease, inside, "request_form", "nested_connect")
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			e.rejectInvalid(w, r, lease, inside, "request_form", "connect_body")
			return
		}
		e.connect(w, r, lease, bearer, started)
		return
	}
	e.mu.Lock()
	e.streams++
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.streams--; e.mu.Unlock() }()
	raw := r.RequestURI
	for _, b := range []byte(raw) {
		if b < 0x21 || b > 0x7e {
			e.rejectInvalid(w, r, lease, inside, "target", "invalid_target_syntax")
			return
		}
	}
	sni := ""
	var destination *httppolicy.Destination
	if inside != nil {
		destination = &inside.destination
		sni = inside.sni
		if r.URL.IsAbs() || !strings.HasPrefix(raw, "/") {
			e.rejectInvalid(w, r, lease, inside, "request_form", "origin_form_required")
			return
		}
		raw = "https://" + destination.Authority() + raw
	} else if r.URL.Scheme != "http" {
		e.rejectInvalid(w, r, lease, inside, "request_form", "absolute_http_required")
		return
	}
	target, err := httppolicy.ParseRequest(raw, r.Method, r.Host, sni, destination)
	if err != nil {
		e.rejectInvalid(w, r, lease, inside, "target", httppolicy.RejectionReason(err))
		return
	}
	repository, profile, git, err := e.options.Authority.ResolveGitRequest(r.Context(), target)
	if err != nil {
		if git {
			reason := "unsupported"
			if errors.Is(err, authorization.ErrNotFound) {
				reason = "repository_unavailable"
			}
			e.rejectGit(w, r, lease, reason)
		} else {
			e.observeRejection(started, diagnostics.ProxyRouting, proxyFailureCause(err), err, w)
			reject(w, http.StatusServiceUnavailable)
		}
		return
	}
	address, err := e.options.Remote.ResolveProxy(r.Context(), target.Destination(), e.options.Listeners)
	if err != nil {
		if git {
			e.rejectGit(w, r, lease, "destination_unavailable", upstreamStatus(err))
		} else {
			e.observeRejection(started, diagnostics.ProxyResolution, proxyFailureCause(err), err, w)
			reject(w, upstreamStatus(err))
		}
		return
	}
	if git {
		e.git(w, r, lease, target, address, repository, profile)
		return
	}
	identity, _ := e.options.Evidence.PrepareIdentity()
	result, err := e.options.Admissions.AdmitHTTP(r.Context(), lease, identity, authorization.HTTPAccessInput{PrincipalID: binding.PrincipalID, URL: target.URL().String(), Method: target.Method()}, address.Facts(), e.options.Materials, admissionContext(inside))
	if err != nil || !result.DispatchAuthorized {
		if err != nil {
			e.observeRejection(started, result.FailureStage, result.FailureCause, err, w)
			if result.FailureCause == diagnostics.Capacity {
				rejectCapacity(w, http.StatusServiceUnavailable)
			} else {
				reject(w, http.StatusServiceUnavailable)
			}
			return
		}
		if result.Execution.Reason == contract.HTTPReasonCredentialUnavailable {
			reject(w, http.StatusServiceUnavailable)
		} else {
			reject(w, http.StatusForbidden)
		}
		return
	}
	e.options.Observations.Latency(diagnostics.HTTP, diagnostics.AdmissionStage, time.Since(started))
	e.options.Observations.Execution(diagnostics.HTTP)
	executionStarted := time.Now()
	completion := contract.HTTPTrafficCompletion{Outcome: "prestart_failure"}
	defer func() {
		e.observeCompletion(diagnostics.HTTP, completion.Outcome, executionStarted)
		e.complete(result, identity, completion)
	}()
	rejectResponse := func(status int) {
		completion.ResponseSource = "gateway"
		completion.GatewayStatus = status
		reject(w, status)
	}
	header := r.Header.Clone()
	if result.Material != nil {
		header, err = result.Material.Headers(target, header)
		if err != nil {
			rejectResponse(http.StatusServiceUnavailable)
			return
		}
	}
	stripHopHeaders(header)
	if remote.ValidateProxyHeaders(header) != nil {
		rejectResponse(http.StatusBadRequest)
		return
	}
	controller := http.NewResponseController(w)
	if r.ProtoMajor == 1 {
		// Otherwise net/http locks/drains the unread upload before flushing an
		// early upstream response, deadlocking against the transport's reader.
		if err := controller.EnableFullDuplex(); err != nil {
			rejectResponse(http.StatusBadGateway)
			return
		}
	}
	outgoingBody := r.Body
	if r.Body != http.NoBody {
		requestBody := &countedBody{ReadCloser: r.Body, controller: controller, done: make(chan struct{})}
		defer func() { _ = requestBody.Close(); completion.BytesSent = requestBody.count.Load() }()
		outgoingBody = requestBody
	}
	completion.Outcome = "outcome_unknown"
	response, err := address.ProxyExchange(r.Context(), target, header, outgoingBody, r.ContentLength, result.Execution.PrivateNetwork, e.roots)
	if err != nil {
		completion.Termination = termination(r.Context(), "exchange", err)
		e.observeFailure(w, diagnostics.ProxyExchange, err)
		rejectResponse(upstreamStatus(err))
		return
	}
	defer func() { _ = response.Body.Close() }()
	stripHopHeaders(response.Header)
	for name, values := range response.Header {
		w.Header()[name] = values
	}
	w.WriteHeader(response.StatusCode)
	completion.ResponseSource = "upstream"
	completion.Status = response.StatusCode
	if r.Method == http.MethodHead {
		// H2 HEAD headers end the stream; explicitly flushing can report its
		// normal closure as an error. Leave bounded finalization to the server.
		if err := controller.SetWriteDeadline(time.Now().Add(contract.HTTPProxyIdleTimeout)); err != nil {
			completion.Termination = termination(r.Context(), "deadline", err)
			e.observeFailure(w, diagnostics.ProxyDeadline, err)
			panic(http.ErrAbortHandler)
		}
		if err := r.Context().Err(); err != nil {
			completion.Termination = termination(r.Context(), "response_headers", err)
			e.observeFailure(w, diagnostics.ProxyWrite, err)
			panic(http.ErrAbortHandler)
		}
		completion.Termination = &contract.HTTPTermination{Stage: "response_headers", Condition: "clean"}
		completion.Outcome = "succeeded"
		return
	}
	writer := &streamWriter{writer: w, controller: controller}
	if err := writer.Flush(); err != nil {
		completion.Termination = termination(r.Context(), "downstream_flush", err)
		e.observeTransfer(w, "downstream_flush", err)
		panic(http.ErrAbortHandler)
	}
	n, err := io.CopyBuffer(writer, response.Body, make([]byte, contract.HTTPProxyBufferBytes))
	completion.BytesReceived = n
	if err != nil {
		completion.Termination = termination(r.Context(), "upstream_read", err)
		e.observeTransfer(w, "upstream_read", err)
		panic(http.ErrAbortHandler)
	}
	completion.Termination = &contract.HTTPTermination{Stage: "complete", Condition: "clean"}
	completion.Status = response.StatusCode
	completion.Outcome = "succeeded"
}

func (e *Engine) rejectInvalid(w http.ResponseWriter, r *http.Request, lease *authorization.Lease, inside *intercepted, stage, reason string) {
	metadata := admissionContext(inside)
	metadata.Rejection = &contract.HTTPRejection{Stage: stage, Reason: reason}
	identity, _ := e.options.Evidence.PrepareIdentity()
	_, err := e.options.Admissions.AdmitHTTP(r.Context(), lease, identity, authorization.HTTPAccessInput{PrincipalID: lease.Binding().PrincipalID}, httppolicy.AddressFacts{}, e.options.Materials, metadata)
	if err != nil {
		reject(w, http.StatusServiceUnavailable)
		return
	}
	reject(w, http.StatusBadRequest)
}

func (e *Engine) complete(result invocation.HTTPAdmissionResult, identity invocation.PreparedAdmission, completion contract.HTTPTrafficCompletion) {
	result.Settle()
	now := e.options.Now().UTC()
	start, err := time.Parse(time.RFC3339Nano, identity.AdmittedAt)
	if err != nil {
		return
	}
	// Traffic evidence requires nine fractional digits, even on coarse clocks.
	completion.CompletedAt = now.Format("2006-01-02T15:04:05.000000000Z07:00")
	completion.DurationMS = max(0, now.Sub(start).Milliseconds())
	ctx, cancel := context.WithTimeout(context.Background(), contract.HTTPProxyDrainTimeout)
	defer cancel()
	_ = e.options.Admissions.CompleteHTTP(ctx, result, completion)
}

func rejectCapacity(w http.ResponseWriter, status int) {
	rejectReason(w, status, "connection_limit_reached")
}

func reject(w http.ResponseWriter, status int) {
	rejectReason(w, status, contract.HTTPProxyFailureReason(status))
}

func rejectReason(w http.ResponseWriter, status int, reason string) {
	if tracked, ok := w.(*failureWriter); ok {
		if tracked.started {
			panic(http.ErrAbortHandler)
		}
		if tracked.id != "" {
			w.Header().Set(contract.HTTPProxyCorrelationHeader, tracked.id)
		}
	}
	w.Header().Set("Proxy-Status", "AgentGateway; error="+reason)
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(contract.HTTPProxyDrainTimeout))
	_ = controller.SetWriteDeadline(time.Now().Add(contract.HTTPProxyDrainTimeout))
	if status == http.StatusProxyAuthRequired {
		w.Header().Set("Proxy-Authenticate", `Basic realm="Agent Gateway"`)
	}
	w.Header().Set("Connection", "close")
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, http.StatusText(status), status)
}
func hasConnectionToken(header http.Header, token string) bool {
	for _, v := range header.Values("Connection") {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}
func stripHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Proxy-Authorization", "Proxy-Authenticate", "Proxy-Connection", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade", "Proxy-Status", contract.HTTPProxyCorrelationHeader, contract.HTTPProxyConnectionHeader} {
		header.Del(name)
	}
}

type countedBody struct {
	io.ReadCloser
	mu         sync.Mutex
	closing    bool
	reads      sync.WaitGroup
	done       chan struct{}
	count      atomic.Int64
	eof        atomic.Bool
	controller *http.ResponseController
}

func (b *countedBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	err := b.controller.SetReadDeadline(time.Now().Add(contract.HTTPProxyIdleTimeout))
	b.reads.Add(1)
	b.mu.Unlock()
	defer b.reads.Done()
	if err != nil {
		return 0, err
	}
	n, err := b.ReadCloser.Read(p)
	b.count.Add(int64(n))
	if errors.Is(err, io.EOF) {
		b.eof.Store(true)
	}
	return n, err
}

func (b *countedBody) Close() error {
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		<-b.done
		return nil
	}
	b.closing = true
	// Interrupt a concurrent read before Close can wait on net/http's body lock.
	if !b.eof.Load() {
		_ = b.controller.SetReadDeadline(time.Now())
	}
	b.mu.Unlock()
	err := b.ReadCloser.Close()
	b.reads.Wait()
	_ = b.controller.SetReadDeadline(time.Time{})
	close(b.done)
	return err
}

type streamWriter struct {
	writer     http.ResponseWriter
	controller *http.ResponseController
}

func (w *streamWriter) Flush() error {
	if err := w.controller.SetWriteDeadline(time.Now().Add(contract.HTTPProxyIdleTimeout)); err != nil {
		return transferFailure("deadline", err)
	}
	if err := w.controller.Flush(); err != nil {
		return transferFailure("downstream_flush", err)
	}
	return transferFailure("deadline", w.controller.SetWriteDeadline(time.Time{}))
}

func (w *streamWriter) Write(p []byte) (int, error) {
	if err := w.controller.SetWriteDeadline(time.Now().Add(contract.HTTPProxyIdleTimeout)); err != nil {
		return 0, transferFailure("deadline", err)
	}
	n, err := w.writer.Write(p)
	if err != nil {
		return n, transferFailure("downstream_write", err)
	}
	if err = w.controller.Flush(); err != nil {
		return n, transferFailure("downstream_flush", err)
	}
	if err = w.controller.SetWriteDeadline(time.Time{}); err != nil {
		return n, transferFailure("deadline", err)
	}
	if n != len(p) {
		return n, transferFailure("downstream_write", io.ErrShortWrite)
	}
	return n, nil
}

// Check the canonical SNI against CONNECT, without creating another parser.
func checkSNI(d httppolicy.Destination, hello *tls.ClientHelloInfo) error {
	if hello.ServerName == "" {
		return nil
	}
	candidate, err := httppolicy.NewDestination(hello.ServerName, d.Port())
	if err != nil || candidate != d {
		return ErrUnavailable
	}
	return nil
}
