package composition

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/oauth"
)

var errCallbackListener = oauth.ErrCallbackUnavailable

type oauthCallbackListeners struct {
	mu          sync.Mutex
	closed      bool
	leases      map[string]*oauthCallbackListener
	changed     chan struct{}
	connections chan struct{}
	handle      func(context.Context, string, string, string) oauth.CallbackResult
	notify      func(context.Context, oauth.CallbackResult)
	now         func() time.Time
}

type oauthCallbackListener struct {
	once     sync.Once
	listener net.Listener
	server   *http.Server
	timer    *time.Timer
}

func newOAuthCallbackListeners(built *Composition, options Options) *oauthCallbackListeners {
	bound, _ := contract.FixedLimitByName("oauth_flows")
	return &oauthCallbackListeners{leases: make(map[string]*oauthCallbackListener), changed: make(chan struct{}), connections: make(chan struct{}, int(bound.Maximum)), now: options.Clock.Now,
		handle: func(ctx context.Context, query, uri, id string) oauth.CallbackResult {
			return built.flows.HandleCallbackAt(ctx, query, uri, id)
		},
		notify: func(ctx context.Context, result oauth.CallbackResult) {
			if result.FlowID != "" {
				options.Invalidate(contract.Invalidation{Kind: contract.InvalidationServerAuthFlows, ResourceID: &result.FlowID})
			}
			if result.ServerID != "" {
				options.Invalidate(contract.Invalidation{Kind: contract.InvalidationServers, ResourceID: &result.ServerID})
				built.TriggerServer(audit.WithCause(ctx, result.Cause), result.ServerID, nil, true)
			}
		},
	}
}

func (owner *oauthCallbackListeners) AcquireCallback(ctx context.Context, id, uri string, expires time.Time) (func(), error) {
	parsed, address, err := contract.ParseOAuthCallbackURI(uri)
	if err != nil || id == "" || ctx.Err() != nil || !expires.After(owner.now()) {
		return nil, errCallbackListener
	}
	owner.mu.Lock()
	bound, _ := contract.FixedLimitByName("oauth_flows")
	if owner.closed || int64(len(owner.leases)) >= bound.Maximum || owner.leases[id] != nil {
		owner.mu.Unlock()
		return nil, errCallbackListener
	}
	lease := &oauthCallbackListener{}
	owner.leases[id] = lease
	owner.mu.Unlock()
	config := net.ListenConfig{}
	listener, err := config.Listen(ctx, "tcp", address)
	if err != nil {
		owner.mu.Lock()
		delete(owner.leases, id)
		owner.signal()
		owner.mu.Unlock()
		return nil, errCallbackListener
	}
	lease.listener = &callbackBoundListener{Listener: listener, slots: owner.connections}
	lease.server = &http.Server{
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16 * 1024,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recover() != nil {
					owner.release(id, lease)
					// net/http's default panic logger must never receive callback secrets.
					oauth.WriteCallbackResponse(w, oauth.CallbackTransient)
				}
			}()
			w.Header().Set("Connection", "close")
			if r.Method != http.MethodGet || r.Host != parsed.Host || r.URL.IsAbs() || r.URL.Path != parsed.Path || r.URL.RawPath != "" || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.URL.RawQuery) > 8192 || len(r.Header) > 64 {
				oauth.WriteCallbackResponse(w, oauth.CallbackInvalid)
				return
			}
			for name := range r.Header {
				lower := strings.ToLower(name)
				if lower == "forwarded" || strings.HasPrefix(lower, "x-forwarded-") {
					oauth.WriteCallbackResponse(w, oauth.CallbackInvalid)
					return
				}
			}
			workCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			result := owner.handle(workCtx, r.URL.RawQuery, uri, id)
			owner.notify(workCtx, result)
			oauth.WriteCallbackResponse(w, result.Outcome)
		}),
	}
	release := func() { owner.release(id, lease) }
	owner.mu.Lock()
	if owner.closed || ctx.Err() != nil {
		delete(owner.leases, id)
		owner.signal()
		owner.mu.Unlock()
		_ = listener.Close()
		return nil, errCallbackListener
	}
	lease.timer = time.AfterFunc(expires.Sub(owner.now()), release)
	go func() { _ = lease.server.Serve(lease.listener); release() }()
	owner.mu.Unlock()
	return release, nil
}

func (owner *oauthCallbackListeners) release(id string, lease *oauthCallbackListener) {
	lease.once.Do(func() {
		owner.mu.Lock()
		if lease.timer != nil {
			lease.timer.Stop()
		}
		owner.mu.Unlock()
		_ = lease.listener.Close()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if lease.server.Shutdown(ctx) != nil {
				_ = lease.server.Close()
			}
			owner.mu.Lock()
			delete(owner.leases, id)
			owner.signal()
			owner.mu.Unlock()
		}()
	})
}

func (owner *oauthCallbackListeners) signal() {
	close(owner.changed)
	owner.changed = make(chan struct{})
}

func (owner *oauthCallbackListeners) Shutdown() {
	owner.mu.Lock()
	owner.closed = true
	var leases []*oauthCallbackListener
	var ids []string
	for id, lease := range owner.leases {
		// An in-flight bind observes closed before publication and closes its own listener.
		if lease.timer != nil {
			ids = append(ids, id)
			leases = append(leases, lease)
		}
	}
	owner.mu.Unlock()
	for index, lease := range leases {
		owner.release(ids[index], lease)
	}
}

func (owner *oauthCallbackListeners) Wait(ctx context.Context) bool {
	owner.Shutdown()
	for {
		owner.mu.Lock()
		done := len(owner.leases) == 0
		changed := owner.changed
		owner.mu.Unlock()
		if done {
			return true
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
	}
}

type callbackBoundListener struct {
	net.Listener
	slots chan struct{}
}
type callbackBoundConn struct {
	net.Conn
	once  sync.Once
	slots chan struct{}
}

func (listener *callbackBoundListener) Accept() (net.Conn, error) {
	for {
		conn, err := listener.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case listener.slots <- struct{}{}:
			return &callbackBoundConn{Conn: conn, slots: listener.slots}, nil
		default:
			_ = conn.Close()
		}
	}
}
func (conn *callbackBoundConn) Close() error {
	err := conn.Conn.Close()
	conn.once.Do(func() { <-conn.slots })
	return err
}
