package keyring

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	oskeyring "github.com/zalando/go-keyring"
)

const (
	gatewayServicePrefix       = "com.github.averycrespi.mcp-gateway."
	providerItemMaximumBytes   = 512
	installationIdentifierSize = 26
)

var (
	ErrNotFound       = errors.New("keyring item was not found")
	ErrSecretTooLarge = errors.New("secret exceeds the keyring item limit")
	ErrWorkLimit      = errors.New("keyring work limit is reached")

	errBackendAbsent              = errors.New("credential backend is absent")
	errBackendLocked              = errors.New("credential backend is locked")
	errBackendInteractionRequired = errors.New("credential backend requires interaction")
)

type Remediation string

const (
	RemediationNone        Remediation = "none"
	RemediationInstall     Remediation = "install"
	RemediationUnlock      Remediation = "unlock"
	RemediationAuthorize   Remediation = "authorize_interaction"
	RemediationRetry       Remediation = "retry"
	RemediationUnsupported Remediation = "unsupported_platform"
)

type Capability struct {
	State       contract.KeyringCapability
	Remediation Remediation
}

func (capability Capability) String() string {
	return "keyring capability is " + string(capability.State)
}

type CapabilityError struct {
	Capability Capability
}

func (failure *CapabilityError) Error() string {
	return failure.Capability.String()
}

type RecordKind string

const (
	RecordStaticCredential RecordKind = "static_credential" //nolint:gosec // Public record kind, not secret material.
	RecordOAuthClient      RecordKind = "oauth_client"      //nolint:gosec // Public record kind, not secret material.
	RecordOAuthTokens      RecordKind = "oauth_tokens"      //nolint:gosec // Public record kind, not secret material.
)

type Namespace struct {
	installationID string
	owner          string
	kind           RecordKind
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

type adapter interface {
	Probe(context.Context, string) error
	Set(service, user, password string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

type workLimiter struct {
	slot chan struct{}
}

func newWorkLimiter() *workLimiter {
	return &workLimiter{slot: make(chan struct{}, 1)}
}

var globalWorkLimiter = newWorkLimiter()

type Provider struct {
	installationID string
	service        string
	adapter        adapter
	work           *workLimiter
}

func NewProvider(installationID string) (*Provider, error) {
	return newProviderWithAdapterAndLimiter(installationID, systemAdapter{}, globalWorkLimiter)
}

func newProviderWithAdapter(installationID string, backend adapter) (*Provider, error) {
	return newProviderWithAdapterAndLimiter(installationID, backend, newWorkLimiter())
}

func newProviderWithAdapterAndLimiter(installationID string, backend adapter, limiter *workLimiter) (*Provider, error) {
	if !validInstallationID(installationID) {
		return nil, fmt.Errorf("invalid keyring installation identifier")
	}
	if backend == nil {
		return nil, fmt.Errorf("keyring adapter is required")
	}
	if limiter == nil {
		return nil, fmt.Errorf("keyring work limiter is required")
	}
	return &Provider{
		installationID: installationID,
		service:        gatewayServicePrefix + installationID,
		adapter:        backend,
		work:           limiter,
	}, nil
}

func (provider *Provider) Probe(ctx context.Context) Capability {
	return capabilityForError(provider.adapter.Probe(ctx, provider.service))
}

func (provider *Provider) set(ctx context.Context, namespace Namespace, item, value string) error {
	if err := provider.validate(namespace, item); err != nil {
		return err
	}
	if value == "" {
		return fmt.Errorf("keyring value is empty")
	}
	release, err := provider.acquireWork()
	if err != nil {
		return err
	}
	defer release()
	if err := provider.requireReady(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return &CapabilityError{Capability: capabilityForError(err)}
	}
	return classifyOperationError(provider.adapter.Set(provider.service, item, value))
}

func (provider *Provider) get(ctx context.Context, namespace Namespace, item string) (string, error) {
	if err := provider.validate(namespace, item); err != nil {
		return "", err
	}
	release, err := provider.acquireWork()
	if err != nil {
		return "", err
	}
	defer release()
	if err := provider.requireReady(ctx); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", &CapabilityError{Capability: capabilityForError(err)}
	}
	value, err := provider.adapter.Get(provider.service, item)
	if err != nil {
		return "", classifyOperationError(err)
	}
	if value == "" {
		return "", &CapabilityError{Capability: capabilityForError(errors.New("empty keyring value"))}
	}
	return value, nil
}

func (provider *Provider) delete(ctx context.Context, namespace Namespace, item string) error {
	if err := provider.validate(namespace, item); err != nil {
		return err
	}
	release, err := provider.acquireWork()
	if err != nil {
		return err
	}
	defer release()
	if err := provider.requireReady(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return &CapabilityError{Capability: capabilityForError(err)}
	}
	return classifyOperationError(provider.adapter.Delete(provider.service, item))
}

func (provider *Provider) WorkStatus() contract.LimitStatus {
	return provider.work.status()
}

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

func (provider *Provider) requireReady(ctx context.Context) error {
	capability := provider.Probe(ctx)
	if capability.State != contract.KeyringReady {
		return &CapabilityError{Capability: capability}
	}
	return nil
}

func (provider *Provider) validate(namespace Namespace, item string) error {
	if namespace.installationID != provider.installationID || !validInstallationID(namespace.owner) || !validRecordKind(namespace.kind) {
		return fmt.Errorf("keyring namespace does not belong to this provider")
	}
	if item == "" || len(item) > providerItemMaximumBytes || !validItem(item) {
		return fmt.Errorf("invalid keyring item identifier")
	}
	return nil
}

func capabilityForError(err error) Capability {
	switch {
	case err == nil:
		return Capability{State: contract.KeyringReady, Remediation: RemediationNone}
	case errors.Is(err, errBackendAbsent):
		return Capability{State: contract.KeyringAbsent, Remediation: RemediationInstall}
	case errors.Is(err, errBackendLocked):
		return Capability{State: contract.KeyringLocked, Remediation: RemediationUnlock}
	case errors.Is(err, errBackendInteractionRequired):
		return Capability{State: contract.KeyringInteractionRequired, Remediation: RemediationAuthorize}
	case errors.Is(err, oskeyring.ErrUnsupportedPlatform):
		return Capability{State: contract.KeyringUnsupported, Remediation: RemediationUnsupported}
	default:
		return Capability{State: contract.KeyringUnavailable, Remediation: RemediationRetry}
	}
}

func classifyOperationError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, oskeyring.ErrNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, oskeyring.ErrSetDataTooBig) {
		return ErrSecretTooLarge
	}
	return &CapabilityError{Capability: capabilityForError(err)}
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
	return kind == RecordStaticCredential || kind == RecordOAuthClient || kind == RecordOAuthTokens
}

func validItem(value string) bool {
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

type systemAdapter struct{}

func (systemAdapter) Probe(ctx context.Context, service string) error {
	return probeSystem(ctx, service)
}

func (systemAdapter) Set(service, user, password string) error {
	return oskeyring.Set(service, user, password)
}

func (systemAdapter) Get(service, user string) (string, error) {
	return oskeyring.Get(service, user)
}

func (systemAdapter) Delete(service, user string) error {
	return oskeyring.Delete(service, user)
}
