package admin

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

var (
	ErrSessionLimit         = errors.New("admin session limit is reached")
	ErrCSRF                 = errors.New("CSRF validation failed")
	ErrAmbiguousCredentials = errors.New("multiple credential types were supplied")
	ErrShuttingDown         = errors.New("admin session registry is shutting down")
)

type CreatedSession struct {
	ID                string
	CSRFToken         string
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	Done              <-chan struct{}
}

func (session CreatedSession) Cookie() http.Cookie {
	return http.Cookie{ //nolint:gosec // The exact plain-loopback HTTP contract intentionally omits Secure.
		Name:     contract.SessionCookieName,
		Value:    session.ID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}

type activeSession struct {
	id             string
	csrf           string
	parentID       string
	lastActivity   time.Time
	absoluteExpiry time.Time
	done           chan struct{}
}

type SessionManager struct {
	mu          sync.Mutex
	service     *Service
	clock       Clock
	entropy     io.Reader
	limit       int64
	sessions    map[string]*activeSession
	reserved    int64
	shutting    bool
	unsubscribe func()
}

func NewSessionManager(service *Service, clock Clock, entropy io.Reader) *SessionManager {
	limit, ok := contract.FixedLimitByName("admin_sessions")
	if !ok {
		panic("admin_sessions contract limit is missing")
	}
	manager := &SessionManager{
		service:  service,
		clock:    clock,
		entropy:  entropy,
		limit:    limit.Maximum,
		sessions: make(map[string]*activeSession),
	}
	manager.unsubscribe = service.SubscribeCredentialInvalidations(manager.invalidateCredential)
	return manager
}

func (manager *SessionManager) Exchange(ctx context.Context, bearer string) (CreatedSession, error) {
	if _, err := manager.service.Authenticate(ctx, bearer); err != nil {
		return CreatedSession{}, err
	}
	if err := manager.reserve(); err != nil {
		return CreatedSession{}, err
	}
	reserved := true
	defer func() {
		if reserved {
			manager.releaseReservation()
		}
	}()

	id, err := randomSessionValue(manager.entropy)
	if err != nil {
		return CreatedSession{}, err
	}
	csrf, err := randomSessionValue(manager.entropy)
	if err != nil {
		return CreatedSession{}, err
	}
	now := manager.clock.Now()

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.shutting {
		return CreatedSession{}, ErrShuttingDown
	}
	parent, err := manager.service.Authenticate(ctx, bearer)
	if err != nil {
		return CreatedSession{}, err
	}
	if _, exists := manager.sessions[id]; exists {
		return CreatedSession{}, fmt.Errorf("generated duplicate admin session ID")
	}
	done := make(chan struct{})
	session := &activeSession{
		id:             id,
		csrf:           csrf,
		parentID:       parent.ID,
		lastActivity:   now,
		absoluteExpiry: now.Add(contract.AdminSessionAbsoluteLifetime),
		done:           done,
	}
	manager.sessions[id] = session
	manager.reserved--
	reserved = false
	return createdSession(session), nil
}

func (manager *SessionManager) Bootstrap(ctx context.Context, sessionID string) (CreatedSession, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	session, ok := manager.sessions[sessionID]
	if !ok || manager.shutting {
		return CreatedSession{}, ErrAuthenticationRequired
	}
	now := manager.clock.Now()
	if manager.expired(session, now) {
		manager.closeSession(session)
		return CreatedSession{}, ErrAuthenticationRequired
	}
	parent, err := manager.service.Get(ctx, session.parentID)
	if err != nil || parent.Status != contract.CredentialActive {
		manager.closeSession(session)
		return CreatedSession{}, ErrAuthenticationRequired
	}
	session.lastActivity = now
	return createdSession(session), nil
}

func (manager *SessionManager) Authenticate(
	ctx context.Context,
	bearer string,
	sessionID string,
	csrf string,
	requireCSRF bool,
) (contract.AdminCredential, error) {
	if bearer != "" && sessionID != "" {
		return contract.AdminCredential{}, ErrAmbiguousCredentials
	}
	if bearer != "" {
		return manager.service.Authenticate(ctx, bearer)
	}
	if sessionID == "" {
		return contract.AdminCredential{}, ErrAuthenticationRequired
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	session, ok := manager.sessions[sessionID]
	if !ok || manager.shutting {
		return contract.AdminCredential{}, ErrAuthenticationRequired
	}
	now := manager.clock.Now()
	if manager.expired(session, now) {
		manager.closeSession(session)
		return contract.AdminCredential{}, ErrAuthenticationRequired
	}
	parent, err := manager.service.Get(ctx, session.parentID)
	if err != nil || parent.Status != contract.CredentialActive {
		manager.closeSession(session)
		return contract.AdminCredential{}, ErrAuthenticationRequired
	}
	if requireCSRF && !constantTimeEqual(session.csrf, csrf) {
		return contract.AdminCredential{}, ErrCSRF
	}
	session.lastActivity = now
	return parent, nil
}

func (manager *SessionManager) Subscribe(sessionID string) (<-chan struct{}, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	session, ok := manager.sessions[sessionID]
	if !ok || manager.shutting {
		return nil, ErrAuthenticationRequired
	}
	return session.done, nil
}

func (manager *SessionManager) Logout(sessionID string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	session, ok := manager.sessions[sessionID]
	if !ok {
		return ErrAuthenticationRequired
	}
	manager.closeSession(session)
	return nil
}

func (manager *SessionManager) Sweep(ctx context.Context) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	now := manager.clock.Now()
	for _, session := range manager.sessions {
		if manager.expired(session, now) {
			manager.closeSession(session)
			continue
		}
		parent, err := manager.service.Get(ctx, session.parentID)
		if err != nil || parent.Status != contract.CredentialActive {
			manager.closeSession(session)
		}
	}
}

func (manager *SessionManager) Status() contract.LimitStatus {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	inUse := int64(len(manager.sessions)) + manager.reserved
	return contract.LimitStatus{InUse: inUse, Limit: manager.limit, Saturated: inUse >= manager.limit}
}

func (manager *SessionManager) Shutdown() {
	manager.mu.Lock()
	if manager.shutting {
		manager.mu.Unlock()
		return
	}
	manager.shutting = true
	for _, session := range manager.sessions {
		manager.closeSession(session)
	}
	unsubscribe := manager.unsubscribe
	manager.unsubscribe = nil
	manager.mu.Unlock()
	if unsubscribe != nil {
		unsubscribe()
	}
}

func (manager *SessionManager) reserve() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.shutting {
		return ErrShuttingDown
	}
	if int64(len(manager.sessions))+manager.reserved >= manager.limit {
		return ErrSessionLimit
	}
	manager.reserved++
	return nil
}

func (manager *SessionManager) releaseReservation() {
	manager.mu.Lock()
	manager.reserved--
	manager.mu.Unlock()
}

func (manager *SessionManager) invalidateCredential(id *string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for _, session := range manager.sessions {
		if id == nil || session.parentID == *id {
			manager.closeSession(session)
		}
	}
}

func (manager *SessionManager) closeSession(session *activeSession) {
	if _, exists := manager.sessions[session.id]; !exists {
		return
	}
	delete(manager.sessions, session.id)
	close(session.done)
}

func (manager *SessionManager) expired(session *activeSession, now time.Time) bool {
	return !now.Before(session.absoluteExpiry) || !now.Before(session.lastActivity.Add(contract.AdminSessionIdleLifetime))
}

func createdSession(session *activeSession) CreatedSession {
	idleExpiry := session.lastActivity.Add(contract.AdminSessionIdleLifetime)
	if idleExpiry.After(session.absoluteExpiry) {
		idleExpiry = session.absoluteExpiry
	}
	return CreatedSession{
		ID:                session.id,
		CSRFToken:         session.csrf,
		IdleExpiresAt:     idleExpiry,
		AbsoluteExpiresAt: session.absoluteExpiry,
		Done:              session.done,
	}
}

func randomSessionValue(entropy io.Reader) (string, error) {
	value := make([]byte, contract.SessionValueBytes)
	if _, err := io.ReadFull(entropy, value); err != nil {
		return "", fmt.Errorf("generate admin session value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func constantTimeEqual(expected, actual string) bool {
	return len(expected) == len(actual) && subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}
