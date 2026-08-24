package runtimes

import (
	"crypto/sha256"
	"errors"
	"sync"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

var (
	ErrRuntimeOwnerLimit = errors.New("downstream runtime owner limit is reached")
	ErrCandidateOwned    = errors.New("downstream runtime candidate is already owned")
	ErrMaterialLease     = errors.New("runtime material lease is invalid")
)

type CandidateKey struct {
	ServerID                 string
	DesiredState             contract.DesiredServerState
	DesiredRevision          string
	TransportDigest          [sha256.Size]byte
	RegistrationRevision     string
	StaticCredentialRevision string
	OAuthClientRevision      string
	OAuthTokensRevision      string
	RuntimeID                string
	Generation               uint64
	DrainEpoch               uint64
}

func (candidate Candidate) Key() CandidateKey {
	key := CandidateKey{
		ServerID:        candidate.Server.ID,
		DesiredState:    candidate.Server.DesiredState,
		DesiredRevision: candidate.Server.DesiredRevision,
		TransportDigest: sha256.Sum256(candidate.Server.Transport),
		RuntimeID:       candidate.RuntimeID,
		Generation:      candidate.Generation,
		DrainEpoch:      candidate.DrainEpoch,
	}
	transport, err := servers.DecodeTransport(candidate.Server.Transport)
	if err != nil {
		return key
	}
	switch value := transport.(type) {
	case contract.StdioTransport:
		if len(value.SecretEnvironment) != 0 {
			key.StaticCredentialRevision = candidate.Authority.CredentialRevisions.StaticCredential
		}
	case contract.StreamableHTTPTransport:
		switch authentication := value.Authentication.(type) {
		case contract.BearerAuthentication:
			key.StaticCredentialRevision = candidate.Authority.CredentialRevisions.StaticCredential
		case contract.OAuthAuthentication:
			key.RegistrationRevision = candidate.Authority.RegistrationRevision
			key.OAuthTokensRevision = candidate.Authority.CredentialRevisions.OAuthTokens
			if candidate.Authority.OAuthClientHandle != nil {
				key.OAuthClientRevision = candidate.Authority.CredentialRevisions.OAuthClient
			} else if registration, ok := authentication.Registration.(contract.StaticOAuthRegistration); ok && registration.TokenEndpointAuthMethod != contract.TokenEndpointAuthNone {
				key.OAuthClientRevision = candidate.Authority.CredentialRevisions.OAuthClient
			}
		}
	}
	return key
}

type RuntimePhase uint8

const (
	RuntimeConstructing RuntimePhase = iota + 1
	RuntimeNegotiating
	RuntimeCataloging
	RuntimeActive
	RuntimeBlockedStop
)

type OAuthMaterialMetadata struct {
	Scopes         []string
	ScopeSpecified bool
	ExpiresAt      *string
}

type MaterialLease struct {
	mu          sync.Mutex
	key         CandidateKey
	values      map[contract.ServerCredentialKind][]byte
	oauth       *OAuthMaterialMetadata
	transferred bool
	cleared     bool
}

type materialBundle struct {
	mu      sync.Mutex
	values  map[contract.ServerCredentialKind][]byte
	oauth   *OAuthMaterialMetadata
	cleared bool
}

func NewMaterialLease(key CandidateKey, values map[contract.ServerCredentialKind][]byte) (*MaterialLease, error) {
	limit, ok := contract.FixedLimitByName("keyring_secret_bytes")
	if !ok || key.ServerID == "" || key.DesiredRevision == "" || key.RuntimeID == "" || key.Generation == 0 || len(values) == 0 {
		return nil, ErrMaterialLease
	}
	cloned := make(map[contract.ServerCredentialKind][]byte, len(values))
	var total int64
	for kind, value := range values {
		switch kind {
		case contract.ServerCredentialStatic, contract.ServerCredentialOAuthClient, contract.ServerCredentialOAuthTokens:
		default:
			clearMaterial(cloned)
			return nil, ErrMaterialLease
		}
		if len(value) == 0 {
			clearMaterial(cloned)
			return nil, ErrMaterialLease
		}
		total += int64(len(value))
		if total > limit.Maximum {
			clearMaterial(cloned)
			return nil, ErrMaterialLease
		}
		cloned[kind] = append([]byte(nil), value...)
	}
	return &MaterialLease{key: key, values: cloned}, nil
}

func NewOAuthMaterialLease(key CandidateKey, clientSecret, accessToken []byte, metadata OAuthMaterialMetadata) (*MaterialLease, error) {
	values := map[contract.ServerCredentialKind][]byte{contract.ServerCredentialOAuthTokens: accessToken}
	if len(clientSecret) != 0 {
		values[contract.ServerCredentialOAuthClient] = clientSecret
	}
	lease, err := NewMaterialLease(key, values)
	if err != nil {
		return nil, err
	}
	lease.oauth = cloneOAuthMetadata(&metadata)
	return lease, nil
}

func (lease *MaterialLease) transfer(key CandidateKey) (*materialBundle, bool) {
	if lease == nil {
		return &materialBundle{values: make(map[contract.ServerCredentialKind][]byte)}, true
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.transferred || lease.cleared || lease.key != key {
		return nil, false
	}
	bundle := &materialBundle{values: lease.values, oauth: lease.oauth}
	lease.values = nil
	lease.oauth = nil
	lease.transferred = true
	return bundle, true
}

func (*MaterialLease) String() string { return "material-lease" }

func (lease *MaterialLease) Transferred() bool {
	if lease == nil {
		return false
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.transferred
}

func (lease *MaterialLease) Clear() bool {
	if lease == nil {
		return false
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.transferred || lease.cleared {
		return false
	}
	clearMaterial(lease.values)
	lease.values = nil
	lease.oauth = nil
	lease.cleared = true
	return true
}

func (bundle *materialBundle) material(kind contract.ServerCredentialKind) ([]byte, bool) {
	bundle.mu.Lock()
	defer bundle.mu.Unlock()
	if bundle.cleared {
		return nil, false
	}
	value, ok := bundle.values[kind]
	return value, ok
}

func (bundle *materialBundle) oauthMetadata() (OAuthMaterialMetadata, bool) {
	bundle.mu.Lock()
	defer bundle.mu.Unlock()
	if bundle.cleared || bundle.oauth == nil {
		return OAuthMaterialMetadata{}, false
	}
	return *cloneOAuthMetadata(bundle.oauth), true
}

func (bundle *materialBundle) clear() bool {
	bundle.mu.Lock()
	defer bundle.mu.Unlock()
	if bundle.cleared {
		return false
	}
	clearMaterial(bundle.values)
	bundle.values = nil
	bundle.oauth = nil
	bundle.cleared = true
	return true
}

func cloneOAuthMetadata(value *OAuthMaterialMetadata) *OAuthMaterialMetadata {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Scopes = append([]string(nil), value.Scopes...)
	cloned.ExpiresAt = cloneString(value.ExpiresAt)
	return &cloned
}

func clearMaterial(values map[contract.ServerCredentialKind][]byte) {
	for kind, value := range values {
		clear(value)
		delete(values, kind)
	}
}

type RuntimeOwner struct {
	mu       sync.Mutex
	owned    map[CandidateKey]*ownedRuntime
	byServer map[string]CandidateKey
	limit    int64
}

type ownedRuntime struct {
	key      CandidateKey
	phase    RuntimePhase
	material *materialBundle
}

type OwnedRuntime struct {
	key CandidateKey
}

func NewRuntimeOwner() *RuntimeOwner {
	limit, _ := contract.FixedLimitByName("downstream_runtimes")
	return &RuntimeOwner{owned: make(map[CandidateKey]*ownedRuntime), byServer: make(map[string]CandidateKey), limit: limit.Maximum}
}

func (owner *RuntimeOwner) Admit(candidate Candidate, lease *MaterialLease, construct func(OwnedRuntime) error) (CandidateKey, error) {
	key := candidate.Key()
	owner.mu.Lock()
	if _, exists := owner.byServer[key.ServerID]; exists {
		owner.mu.Unlock()
		return CandidateKey{}, ErrCandidateOwned
	}
	if int64(len(owner.owned)) >= owner.limit {
		owner.mu.Unlock()
		return CandidateKey{}, ErrRuntimeOwnerLimit
	}
	material, ok := lease.transfer(key)
	if !ok {
		owner.mu.Unlock()
		return CandidateKey{}, ErrMaterialLease
	}
	owner.owned[key] = &ownedRuntime{key: key, phase: RuntimeConstructing, material: material}
	owner.byServer[key.ServerID] = key
	owner.mu.Unlock()

	if construct != nil {
		if err := construct(OwnedRuntime{key: key}); err != nil {
			return key, err
		}
	}
	return key, nil
}

func (owned OwnedRuntime) Key() CandidateKey { return owned.key }

func (owner *RuntimeOwner) Phase(key CandidateKey) (RuntimePhase, bool) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	owned, ok := owner.owned[key]
	if !ok {
		return 0, false
	}
	return owned.phase, true
}

func (owner *RuntimeOwner) Transition(key CandidateKey, next RuntimePhase) bool {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	owned, ok := owner.owned[key]
	if !ok || !phaseTransitionAllowed(owned.phase, next) {
		return false
	}
	owned.phase = next
	return true
}

func phaseTransitionAllowed(current, next RuntimePhase) bool {
	if current == next {
		return true
	}
	switch current {
	case RuntimeConstructing:
		return next == RuntimeNegotiating
	case RuntimeNegotiating:
		return next == RuntimeCataloging
	case RuntimeCataloging:
		return next == RuntimeActive
	default:
		return false
	}
}

func (owner *RuntimeOwner) Material(key CandidateKey, kind contract.ServerCredentialKind) ([]byte, bool) {
	owner.mu.Lock()
	owned, ok := owner.owned[key]
	owner.mu.Unlock()
	if !ok {
		return nil, false
	}
	return owned.material.material(kind)
}

func (owner *RuntimeOwner) OAuthMetadata(key CandidateKey) (OAuthMaterialMetadata, bool) {
	owner.mu.Lock()
	owned, ok := owner.owned[key]
	owner.mu.Unlock()
	if !ok {
		return OAuthMaterialMetadata{}, false
	}
	return owned.material.oauthMetadata()
}

func (owner *RuntimeOwner) Release(key CandidateKey, verified bool) bool {
	owner.mu.Lock()
	owned, ok := owner.owned[key]
	if !ok {
		owner.mu.Unlock()
		return false
	}
	if !verified {
		owned.phase = RuntimeBlockedStop
		owner.mu.Unlock()
		return false
	}
	delete(owner.owned, key)
	delete(owner.byServer, key.ServerID)
	owner.mu.Unlock()
	owned.material.clear()
	return true
}

func (owner *RuntimeOwner) Status() contract.LimitStatus {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	inUse := int64(len(owner.owned))
	return contract.LimitStatus{InUse: inUse, Limit: owner.limit, Saturated: inUse >= owner.limit}
}
