// Package httpproxy implements the unselected receipt-gated HTTP proxy engine.
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
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
)

var ErrUnavailable = errors.New("HTTP proxy unavailable")

type Options struct {
	Authority  *authorization.Repository
	Evidence   *invocation.Repository
	Admissions *invocation.AdmissionCoordinator
	Materials  *httpcredentials.Service
	Remote     *remote.Factory
	Signer     *httpca.Signer
	Listeners  func() []netip.AddrPort
	Now        func() time.Time
}

type Engine struct {
	options     Options
	mu          sync.Mutex
	draining    bool
	connections map[net.Conn]struct{}
	work        int
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
	e := &Engine{options: options, connections: make(map[net.Conn]struct{}), principals: make(map[string]int), afterFunc: func(d time.Duration, f func()) func() { t := time.AfterFunc(d, f); return func() { t.Stop() } }}
	e.server = e.newServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { e.handle(w, r, nil) }))
	return e, nil
}

func (e *Engine) newServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: contract.HTTPProxyHeaderTimeout, IdleTimeout: contract.HTTPProxyIdleTimeout, MaxHeaderBytes: contract.HTTPProxyHeaderBytes, ErrorLog: diagnostics.HTTPErrorLog()}
}

// Serve consumes an already bound composition-owned listener. No production
// caller selects it in this delivery, and New never binds a socket.
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
}

func (e *Engine) handle(w http.ResponseWriter, r *http.Request, inside *intercepted) {
	// Never allow net/http's default panic logger to receive request material.
	defer func() {
		if recover() != nil {
			panic(http.ErrAbortHandler)
		}
	}()
	if !e.acquire("") {
		reject(w, http.StatusServiceUnavailable)
		return
	}
	defer e.release("")
	bearer := ""
	if inside == nil {
		values := r.Header.Values("Proxy-Authorization")
		if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
			w.Header().Set("Proxy-Authenticate", "Bearer")
			reject(w, http.StatusProxyAuthRequired)
			return
		}
		bearer = strings.TrimPrefix(values[0], "Bearer ")
	} else {
		bearer = inside.bearer
		if len(r.Header.Values("Proxy-Authorization")) != 0 {
			reject(w, http.StatusBadRequest)
			return
		}
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
		reject(w, http.StatusServiceUnavailable)
		return
	}
	defer e.release(binding.PrincipalID)
	if remote.ValidateProxyHeaders(r.Header) != nil || len(r.Trailer) != 0 || r.Header.Get("Upgrade") != "" || hasConnectionToken(r.Header, "upgrade") {
		e.rejectInvalid(w, r, lease)
		return
	}
	if r.Method == http.MethodConnect {
		if inside != nil || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			e.rejectInvalid(w, r, lease)
			return
		}
		e.connect(w, r, lease, bearer)
		return
	}
	raw := r.RequestURI
	sni := ""
	var destination *httppolicy.Destination
	if inside != nil {
		destination = &inside.destination
		sni = inside.sni
		if r.URL.IsAbs() || !strings.HasPrefix(raw, "/") {
			e.rejectInvalid(w, r, lease)
			return
		}
		raw = "https://" + destination.Authority() + raw
	} else if r.URL.Scheme != "http" {
		e.rejectInvalid(w, r, lease)
		return
	}
	target, err := httppolicy.ParseRequest(raw, r.Method, r.Host, sni, destination)
	if err != nil {
		e.rejectInvalid(w, r, lease)
		return
	}
	address, err := e.options.Remote.ResolveProxy(r.Context(), target.Destination(), e.options.Listeners)
	if err != nil {
		reject(w, http.StatusForbidden)
		return
	}
	identity, err := e.options.Evidence.PrepareIdentity()
	if err != nil {
		reject(w, http.StatusServiceUnavailable)
		return
	}
	result, err := e.options.Admissions.AdmitHTTP(r.Context(), lease, identity, authorization.HTTPAccessInput{PrincipalID: binding.PrincipalID, URL: target.URL().String(), Method: target.Method()}, address.Facts(), e.options.Materials)
	if err != nil || !result.DispatchAuthorized {
		reject(w, http.StatusForbidden)
		return
	}
	completion := contract.HTTPTrafficCompletion{Outcome: "prestart_failure"}
	defer func() { e.complete(result, identity, completion) }()
	header := r.Header.Clone()
	if result.Material != nil {
		header, err = result.Material.Headers(target, header)
		if err != nil {
			reject(w, http.StatusBadGateway)
			return
		}
	}
	stripHopHeaders(header)
	if remote.ValidateProxyHeaders(header) != nil {
		reject(w, http.StatusBadRequest)
		return
	}
	controller := http.NewResponseController(w)
	if r.ProtoMajor == 1 {
		// Otherwise net/http locks/drains the unread upload before flushing an
		// early upstream response, deadlocking against the transport's reader.
		if err := controller.EnableFullDuplex(); err != nil {
			reject(w, http.StatusBadGateway)
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
	response, err := address.ProxyExchange(r.Context(), target, header, outgoingBody, r.ContentLength, result.Evidence.Decision.PrivateGrant != nil, e.roots)
	if err != nil {
		reject(w, http.StatusBadGateway)
		return
	}
	defer func() { _ = response.Body.Close() }()
	stripHopHeaders(response.Header)
	for name, values := range response.Header {
		w.Header()[name] = values
	}
	w.WriteHeader(response.StatusCode)
	writer := &streamWriter{writer: w, controller: controller}
	if _, err := writer.Write(nil); err != nil {
		panic(http.ErrAbortHandler)
	}
	n, err := io.CopyBuffer(writer, response.Body, make([]byte, contract.HTTPProxyBufferBytes))
	completion.BytesReceived = n
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	completion.Status = response.StatusCode
	completion.Outcome = "succeeded"
}

func (e *Engine) rejectInvalid(w http.ResponseWriter, r *http.Request, lease *authorization.Lease) {
	identity, err := e.options.Evidence.PrepareIdentity()
	if err == nil {
		_, err = e.options.Admissions.AdmitHTTP(r.Context(), lease, identity, authorization.HTTPAccessInput{PrincipalID: lease.Binding().PrincipalID}, httppolicy.AddressFacts{}, e.options.Materials)
	}
	if err != nil {
		reject(w, http.StatusServiceUnavailable)
		return
	}
	reject(w, http.StatusBadRequest)
}

func (e *Engine) complete(result invocation.HTTPAdmissionResult, identity invocation.PreparedAdmission, completion contract.HTTPTrafficCompletion) {
	now := e.options.Now().UTC()
	start, err := time.Parse(time.RFC3339Nano, identity.AdmittedAt)
	if err != nil {
		return
	}
	completion.CompletedAt = now.Format(time.RFC3339Nano)
	completion.DurationMS = max(0, now.Sub(start).Milliseconds())
	ctx, cancel := context.WithTimeout(context.Background(), contract.HTTPProxyDrainTimeout)
	defer cancel()
	_ = e.options.Admissions.CompleteHTTP(ctx, result, completion)
}

func reject(w http.ResponseWriter, status int) {
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(contract.HTTPProxyDrainTimeout))
	_ = controller.SetWriteDeadline(time.Now().Add(contract.HTTPProxyDrainTimeout))
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
	for _, name := range []string{"Connection", "Proxy-Authorization", "Proxy-Authenticate", "Proxy-Connection", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
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

func (w *streamWriter) Write(p []byte) (int, error) {
	if err := w.controller.SetWriteDeadline(time.Now().Add(contract.HTTPProxyIdleTimeout)); err != nil {
		return 0, err
	}
	n, err := w.writer.Write(p)
	if err == nil {
		err = w.controller.Flush()
	}
	if err == nil {
		err = w.controller.SetWriteDeadline(time.Time{})
	}
	return n, err
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
