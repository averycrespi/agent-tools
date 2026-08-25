package contract

type CredentialStatus string

const (
	CredentialActive  CredentialStatus = "active"
	CredentialRevoked CredentialStatus = "revoked"
	CredentialExpired CredentialStatus = "expired"
)

func CredentialStatuses() []CredentialStatus {
	return []CredentialStatus{CredentialActive, CredentialRevoked, CredentialExpired}
}

type ProcessState string

const (
	ProcessUninitialized ProcessState = "uninitialized"
	ProcessStarting      ProcessState = "starting"
	ProcessReady         ProcessState = "ready"
	ProcessStorageFailed ProcessState = "storage_failed"
	ProcessDraining      ProcessState = "draining"
)

func ProcessStates() []ProcessState {
	return []ProcessState{ProcessUninitialized, ProcessStarting, ProcessReady, ProcessStorageFailed, ProcessDraining}
}

type SQLiteState string

const (
	SQLiteUninitialized SQLiteState = "uninitialized"
	SQLiteReady         SQLiteState = "ready"
	SQLiteLatched       SQLiteState = "latched"
)

func SQLiteStates() []SQLiteState {
	return []SQLiteState{SQLiteUninitialized, SQLiteReady, SQLiteLatched}
}

type KeyringCapability string

const (
	KeyringReady               KeyringCapability = "ready"
	KeyringAbsent              KeyringCapability = "absent"
	KeyringLocked              KeyringCapability = "locked"
	KeyringInteractionRequired KeyringCapability = "interaction_required"
	KeyringUnavailable         KeyringCapability = "unavailable"
	KeyringUnsupported         KeyringCapability = "unsupported"
)

func KeyringCapabilities() []KeyringCapability {
	return []KeyringCapability{KeyringReady, KeyringAbsent, KeyringLocked, KeyringInteractionRequired, KeyringUnavailable, KeyringUnsupported}
}

type BackupState string

const (
	BackupIdle     BackupState = "idle"
	BackupCreating BackupState = "creating"
)

func BackupStates() []BackupState {
	return []BackupState{BackupIdle, BackupCreating}
}

type AgentAuthMode string

const (
	AgentAuthDenyAll              AgentAuthMode = "deny_all"
	AgentAuthPrincipalCredentials AgentAuthMode = "principal_credentials" //nolint:gosec // Public protocol status, not a credential.
)

func AgentAuthModes() []AgentAuthMode {
	return []AgentAuthMode{AgentAuthDenyAll, AgentAuthPrincipalCredentials}
}

type InvalidationKind string

const (
	InvalidationAdminCredentials InvalidationKind = "admin_credentials" //nolint:gosec // Public event kind, not a credential.
	InvalidationSystemStatus     InvalidationKind = "system_status"
	InvalidationBackups          InvalidationKind = "backups"
	InvalidationServers          InvalidationKind = "servers"
	InvalidationServerOperations InvalidationKind = "server_operations"
	InvalidationServerAuthFlows  InvalidationKind = "server_auth_flows"
	InvalidationCatalog          InvalidationKind = "catalog"
	InvalidationAuthorization    InvalidationKind = "authorization"
)

func InvalidationKinds() []InvalidationKind {
	return []InvalidationKind{
		InvalidationAdminCredentials,
		InvalidationSystemStatus,
		InvalidationBackups,
		InvalidationServers,
		InvalidationServerOperations,
		InvalidationServerAuthFlows,
		InvalidationCatalog,
		InvalidationAuthorization,
	}
}
