package keyring

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

const installationIdentifierSize = 26

var (
	ErrNotFound       = errors.New("keyring item was not found")
	ErrSecretTooLarge = errors.New("secret exceeds the keyring item limit")
	ErrWorkLimit      = errors.New("keyring work limit is reached")
)

type Remediation string

const (
	RemediationNone  Remediation = "none"
	RemediationRetry Remediation = "retry"
)

type Capability struct {
	State       contract.KeyringCapability
	Remediation Remediation
}

func (capability Capability) String() string {
	return "keyring capability is " + string(capability.State)
}

type CapabilityError struct{ Capability Capability }

func (failure *CapabilityError) Error() string { return failure.Capability.String() }

type RecordKind string

const (
	RecordGitCredential    RecordKind = "git_credential" //nolint:gosec // Public record kind, not secret material.
	RecordHTTPCA           RecordKind = "http_ca"
	RecordHTTPCredential   RecordKind = "http_credential"   //nolint:gosec // Public record kind, not secret material.
	RecordStaticCredential RecordKind = "static_credential" //nolint:gosec // Public record kind, not secret material.
	RecordOAuthClient      RecordKind = "oauth_client"      //nolint:gosec // Public record kind, not secret material.
	RecordOAuthTokens      RecordKind = "oauth_tokens"      //nolint:gosec // Public record kind, not secret material.
)

type Namespace struct {
	installationID, owner string
	kind                  RecordKind
}

func NewNamespace(installationID, owner string, kind RecordKind) (Namespace, error) {
	if !validInstallationID(installationID) {
		return Namespace{}, fmt.Errorf("invalid keyring installation identifier")
	}
	if !validInstallationID(owner) {
		return Namespace{}, fmt.Errorf("invalid keyring resource owner")
	}
	if !validRecordKind(kind) {
		return Namespace{}, fmt.Errorf("invalid keyring record kind")
	}
	return Namespace{installationID: installationID, owner: owner, kind: kind}, nil
}
func (namespace Namespace) InstallationID() string { return namespace.installationID }
func (namespace Namespace) Owner() string          { return namespace.owner }
func (namespace Namespace) Kind() RecordKind       { return namespace.kind }

type workLimiter struct{ slot chan struct{} }

func newWorkLimiter() *workLimiter { return &workLimiter{slot: make(chan struct{}, 1)} }

// generationCustody keeps coordinator fault fixtures at the generation boundary.
// The sole production implementation is authenticated database custody.
type generationCustody interface {
	write(context.Context, Namespace, Handle, []byte) error
	read(context.Context, Namespace, Handle) ([]byte, error)
	remove(context.Context, Namespace, Handle) error
	capability() Capability
}

type Provider struct {
	custody        generationCustody
	installationID string
	work           *workLimiter
}

func NewProvider(installationID string) (*Provider, error) {
	if !validInstallationID(installationID) {
		return nil, fmt.Errorf("invalid keyring installation identifier")
	}
	return &Provider{installationID: installationID, work: newWorkLimiter()}, nil
}
func (provider *Provider) Probe(context.Context) Capability {
	if provider.custody == nil {
		return Capability{State: contract.KeyringUnavailable, Remediation: RemediationRetry}
	}
	return provider.custody.capability()
}
func (provider *Provider) WorkStatus() contract.LimitStatus { return provider.work.status() }
func (provider *Provider) acquireWork() (func(), error) {
	select {
	case provider.work.slot <- struct{}{}:
		return func() { <-provider.work.slot }, nil
	default:
		return nil, ErrWorkLimit
	}
}
func (limiter *workLimiter) status() contract.LimitStatus {
	limit := mustFixedLimit("keyring_work")
	inUse := int64(len(limiter.slot))
	return contract.LimitStatus{InUse: inUse, Limit: limit, Saturated: inUse >= limit}
}
func (provider *Provider) validate(namespace Namespace) error {
	if namespace.installationID != provider.installationID || !validInstallationID(namespace.owner) || !validRecordKind(namespace.kind) {
		return fmt.Errorf("keyring namespace does not belong to this provider")
	}
	return nil
}
func validInstallationID(value string) bool {
	if len(value) != installationIdentifierSize {
		return false
	}
	for index, character := range value {
		if index == 0 && character > '7' {
			return false
		}
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", character) {
			return false
		}
	}
	return true
}
func validRecordKind(kind RecordKind) bool {
	return kind == RecordGitCredential || kind == RecordHTTPCA || kind == RecordHTTPCredential || kind == RecordStaticCredential || kind == RecordOAuthClient || kind == RecordOAuthTokens
}
