package contract

import "fmt"

type TransportKind string

const (
	TransportStdio          TransportKind = "stdio"
	TransportStreamableHTTP TransportKind = "streamable_http"
)

func ParseTransportKind(value string) (TransportKind, error) {
	return parseClosed(value, []TransportKind{TransportStdio, TransportStreamableHTTP})
}

type ProtocolMode string

const (
	ProtocolAuto   ProtocolMode = "auto"
	ProtocolModern ProtocolMode = "modern"
	ProtocolLegacy ProtocolMode = "legacy"
)

func ParseProtocolMode(value string) (ProtocolMode, error) {
	return parseClosed(value, []ProtocolMode{ProtocolAuto, ProtocolModern, ProtocolLegacy})
}

type AuthenticationMode string

const (
	AuthenticationNone   AuthenticationMode = "none"
	AuthenticationBearer AuthenticationMode = "bearer"
	AuthenticationOAuth  AuthenticationMode = "oauth"
)

func ParseAuthenticationMode(value string) (AuthenticationMode, error) {
	return parseClosed(value, []AuthenticationMode{AuthenticationNone, AuthenticationBearer, AuthenticationOAuth})
}

type RegistrationMode string

const (
	RegistrationStatic  RegistrationMode = "static"
	RegistrationDynamic RegistrationMode = "dynamic"
)

func ParseRegistrationMode(value string) (RegistrationMode, error) {
	return parseClosed(value, []RegistrationMode{RegistrationStatic, RegistrationDynamic})
}

type TokenEndpointAuthMethod string

const (
	TokenEndpointAuthNone              TokenEndpointAuthMethod = "none"
	TokenEndpointAuthClientSecretBasic TokenEndpointAuthMethod = "client_secret_basic"
	TokenEndpointAuthClientSecretPost  TokenEndpointAuthMethod = "client_secret_post"
)

func ParseTokenEndpointAuthMethod(value string) (TokenEndpointAuthMethod, error) {
	return parseClosed(value, []TokenEndpointAuthMethod{TokenEndpointAuthNone, TokenEndpointAuthClientSecretBasic, TokenEndpointAuthClientSecretPost})
}

type DesiredServerState string

const (
	DesiredServerEnabled  DesiredServerState = "enabled"
	DesiredServerDisabled DesiredServerState = "disabled"
	DesiredServerDeleted  DesiredServerState = "deleted"
)

func ParseDesiredServerState(value string) (DesiredServerState, error) {
	return parseClosed(value, []DesiredServerState{DesiredServerEnabled, DesiredServerDisabled, DesiredServerDeleted})
}

type RuntimeState string

const (
	RuntimeInactive               RuntimeState = "inactive"
	RuntimeActivating             RuntimeState = "activating"
	RuntimeActive                 RuntimeState = "active"
	RuntimeStopping               RuntimeState = "stopping"
	RuntimeRetryWait              RuntimeState = "retry_wait"
	RuntimeDegraded               RuntimeState = "degraded"
	RuntimeAuthenticationRequired RuntimeState = "authentication_required"
	RuntimeDeleted                RuntimeState = "deleted"
)

func ParseRuntimeState(value string) (RuntimeState, error) {
	return parseClosed(value, []RuntimeState{RuntimeInactive, RuntimeActivating, RuntimeActive, RuntimeStopping, RuntimeRetryWait, RuntimeDegraded, RuntimeAuthenticationRequired, RuntimeDeleted})
}

type ServerOperationKind string

const (
	OperationActivate              ServerOperationKind = "activate"
	OperationReload                ServerOperationKind = "reload"
	OperationRetry                 ServerOperationKind = "retry"
	OperationRefreshCatalog        ServerOperationKind = "refresh_catalog"
	OperationCredentialReplace     ServerOperationKind = "credential_replace" //nolint:gosec // Public operation kind, not a credential.
	OperationDisable               ServerOperationKind = "disable"
	OperationDelete                ServerOperationKind = "delete"
	OperationDisconnectCredentials ServerOperationKind = "disconnect_credentials"
)

func ParseServerOperationKind(value string) (ServerOperationKind, error) {
	return parseClosed(value, []ServerOperationKind{OperationActivate, OperationReload, OperationRetry, OperationRefreshCatalog, OperationCredentialReplace, OperationDisable, OperationDelete, OperationDisconnectCredentials})
}

func ExplicitServerOperationKinds() []ServerOperationKind {
	return []ServerOperationKind{OperationReload, OperationRetry, OperationRefreshCatalog, OperationDisconnectCredentials}
}

func ParseExplicitServerOperationKind(value string) (ServerOperationKind, error) {
	return parseClosed(value, ExplicitServerOperationKinds())
}

type ServerOperationState string

const (
	OperationScheduled   ServerOperationState = "scheduled"
	OperationRunning     ServerOperationState = "running"
	OperationSucceeded   ServerOperationState = "succeeded"
	OperationFailed      ServerOperationState = "failed"
	OperationCancelled   ServerOperationState = "cancelled"
	OperationSuperseded  ServerOperationState = "superseded"
	OperationInterrupted ServerOperationState = "interrupted"
)

func ParseServerOperationState(value string) (ServerOperationState, error) {
	return parseClosed(value, []ServerOperationState{OperationScheduled, OperationRunning, OperationSucceeded, OperationFailed, OperationCancelled, OperationSuperseded, OperationInterrupted})
}

type ServerCredentialState string

const (
	ServerCredentialNotRequired              ServerCredentialState = "not_required"
	ServerCredentialReady                    ServerCredentialState = "ready"
	ServerCredentialAbsent                   ServerCredentialState = "absent"
	ServerCredentialLocked                   ServerCredentialState = "locked"
	ServerCredentialInteractionRequired      ServerCredentialState = "interaction_required"
	ServerCredentialUnavailable              ServerCredentialState = "unavailable"
	ServerCredentialUnsupported              ServerCredentialState = "unsupported"
	ServerCredentialRefreshing               ServerCredentialState = "refreshing"
	ServerCredentialReauthenticationRequired ServerCredentialState = "reauthentication_required"
	ServerCredentialDisconnecting            ServerCredentialState = "disconnecting"
	ServerCredentialCleanupPending           ServerCredentialState = "cleanup_pending"
)

func ParseServerCredentialState(value string) (ServerCredentialState, error) {
	return parseClosed(value, []ServerCredentialState{ServerCredentialNotRequired, ServerCredentialReady, ServerCredentialAbsent, ServerCredentialLocked, ServerCredentialInteractionRequired, ServerCredentialUnavailable, ServerCredentialUnsupported, ServerCredentialRefreshing, ServerCredentialReauthenticationRequired, ServerCredentialDisconnecting, ServerCredentialCleanupPending})
}

type DescriptorRetiredFilter string

const (
	DescriptorRetiredInclude DescriptorRetiredFilter = "include"
	DescriptorRetiredExclude DescriptorRetiredFilter = "exclude"
	DescriptorRetiredOnly    DescriptorRetiredFilter = "only"
)

func ParseDescriptorRetiredFilter(value string) (DescriptorRetiredFilter, error) {
	return parseClosed(value, []DescriptorRetiredFilter{DescriptorRetiredInclude, DescriptorRetiredExclude, DescriptorRetiredOnly})
}

type DurableCatalogState string

const (
	DurableCatalogEmpty       DurableCatalogState = "empty"
	DurableCatalogCurrent     DurableCatalogState = "current"
	DurableCatalogStale       DurableCatalogState = "stale"
	DurableCatalogUnavailable DurableCatalogState = "unavailable"
	DurableCatalogRetired     DurableCatalogState = "retired"
)

func ParseDurableCatalogState(value string) (DurableCatalogState, error) {
	return parseClosed(value, []DurableCatalogState{DurableCatalogEmpty, DurableCatalogCurrent, DurableCatalogStale, DurableCatalogUnavailable, DurableCatalogRetired})
}

type ActiveCatalogState string

const (
	ActiveCatalogAbsent      ActiveCatalogState = "absent"
	ActiveCatalogRefreshing  ActiveCatalogState = "refreshing"
	ActiveCatalogCurrent     ActiveCatalogState = "current"
	ActiveCatalogStale       ActiveCatalogState = "stale"
	ActiveCatalogUnavailable ActiveCatalogState = "unavailable"
)

func ParseActiveCatalogState(value string) (ActiveCatalogState, error) {
	return parseClosed(value, []ActiveCatalogState{ActiveCatalogAbsent, ActiveCatalogRefreshing, ActiveCatalogCurrent, ActiveCatalogStale, ActiveCatalogUnavailable})
}

type AggregateCatalogState string

const (
	AggregateCatalogEmpty    AggregateCatalogState = "empty"
	AggregateCatalogCurrent  AggregateCatalogState = "current"
	AggregateCatalogDegraded AggregateCatalogState = "degraded"
)

func ParseAggregateCatalogState(value string) (AggregateCatalogState, error) {
	return parseClosed(value, []AggregateCatalogState{AggregateCatalogEmpty, AggregateCatalogCurrent, AggregateCatalogDegraded})
}

type AuthFlowState string

const (
	AuthFlowPreparing        AuthFlowState = "preparing"
	AuthFlowAwaitingCallback AuthFlowState = "awaiting_callback"
	AuthFlowExchanging       AuthFlowState = "exchanging"
	AuthFlowSucceeded        AuthFlowState = "succeeded"
	AuthFlowFailed           AuthFlowState = "failed"
	AuthFlowExpired          AuthFlowState = "expired"
	AuthFlowCancelled        AuthFlowState = "cancelled"
	AuthFlowSuperseded       AuthFlowState = "superseded"
	AuthFlowInterrupted      AuthFlowState = "interrupted"
)

func ParseAuthFlowState(value string) (AuthFlowState, error) {
	return parseClosed(value, []AuthFlowState{AuthFlowPreparing, AuthFlowAwaitingCallback, AuthFlowExchanging, AuthFlowSucceeded, AuthFlowFailed, AuthFlowExpired, AuthFlowCancelled, AuthFlowSuperseded, AuthFlowInterrupted})
}

type ServerCredentialKind string

const (
	ServerCredentialStatic      ServerCredentialKind = "static_credential" //nolint:gosec // Public credential-kind name, not a credential.
	ServerCredentialOAuthClient ServerCredentialKind = "oauth_client"      //nolint:gosec // Public credential-kind name, not a credential.
	ServerCredentialOAuthTokens ServerCredentialKind = "oauth_tokens"      //nolint:gosec // Public credential-kind name, not a credential.
)

func ParseServerCredentialKind(value string) (ServerCredentialKind, error) {
	return parseClosed(value, []ServerCredentialKind{ServerCredentialStatic, ServerCredentialOAuthClient, ServerCredentialOAuthTokens})
}

func CredentialReplacementKinds() []ServerCredentialKind {
	return []ServerCredentialKind{ServerCredentialStatic, ServerCredentialOAuthClient}
}

func ParseCredentialReplacementKind(value string) (ServerCredentialKind, error) {
	return parseClosed(value, CredentialReplacementKinds())
}

type RetiredFilter string

const (
	RetiredInclude RetiredFilter = "include"
	RetiredExclude RetiredFilter = "exclude"
	RetiredOnly    RetiredFilter = "only"
)

func ParseRetiredFilter(value string) (RetiredFilter, error) {
	return parseClosed(value, []RetiredFilter{RetiredInclude, RetiredExclude, RetiredOnly})
}

type PublicReason string

const (
	ReasonConfigurationInvalid       PublicReason = "configuration_invalid"
	ReasonResourceLimit              PublicReason = "resource_limit"
	ReasonConnectivity               PublicReason = "connectivity"
	ReasonTLSFailed                  PublicReason = "tls_failed"
	ReasonProtocolUnsupported        PublicReason = "protocol_unsupported"
	ReasonProtocolInvalid            PublicReason = "protocol_invalid"
	ReasonAuthenticationRejected     PublicReason = "authentication_rejected"
	ReasonCredentialAbsent           PublicReason = "credential_absent" //nolint:gosec // Public reason code, not a credential.
	ReasonKeyringAbsent              PublicReason = "keyring_absent"
	ReasonKeyringLocked              PublicReason = "keyring_locked"
	ReasonKeyringInteractionRequired PublicReason = "keyring_interaction_required"
	ReasonKeyringUnavailable         PublicReason = "keyring_unavailable"
	ReasonKeyringUnsupported         PublicReason = "keyring_unsupported"
	ReasonOAuthRejected              PublicReason = "oauth_rejected"
	ReasonOAuthExpired               PublicReason = "oauth_expired"
	ReasonRegistrationExpired        PublicReason = "registration_expired"
	ReasonProcessExited              PublicReason = "process_exited"
	ReasonOutputLimit                PublicReason = "output_limit"
	ReasonStopUnconfirmed            PublicReason = "stop_unconfirmed"
	ReasonCatalogInvalid             PublicReason = "catalog_invalid"
	ReasonCatalogLimit               PublicReason = "catalog_limit"
	ReasonCatalogStale               PublicReason = "catalog_stale"
	ReasonSuperseded                 PublicReason = "superseded"
	ReasonCancelled                  PublicReason = "cancelled"
	ReasonInterrupted                PublicReason = "interrupted"
	ReasonRevocationFailed           PublicReason = "revocation_failed"
	ReasonRevocationUnsupported      PublicReason = "revocation_unsupported"
	ReasonCleanupPending             PublicReason = "cleanup_pending"
)

func ParsePublicReason(value string) (PublicReason, error) {
	return parseClosed(value, []PublicReason{
		ReasonConfigurationInvalid, ReasonResourceLimit, ReasonConnectivity, ReasonTLSFailed, ReasonProtocolUnsupported,
		ReasonProtocolInvalid, ReasonAuthenticationRejected, ReasonCredentialAbsent, ReasonKeyringAbsent, ReasonKeyringLocked,
		ReasonKeyringInteractionRequired, ReasonKeyringUnavailable, ReasonKeyringUnsupported, ReasonOAuthRejected, ReasonOAuthExpired,
		ReasonRegistrationExpired, ReasonProcessExited, ReasonOutputLimit, ReasonStopUnconfirmed, ReasonCatalogInvalid, ReasonCatalogLimit,
		ReasonCatalogStale, ReasonSuperseded, ReasonCancelled, ReasonInterrupted, ReasonRevocationFailed, ReasonRevocationUnsupported,
		ReasonCleanupPending,
	})
}

func parseClosed[T ~string](value string, accepted []T) (T, error) {
	for _, candidate := range accepted {
		if string(candidate) == value {
			return candidate, nil
		}
	}
	var zero T
	return zero, fmt.Errorf("value %q is not in the closed contract", value)
}
