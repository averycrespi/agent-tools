package contract

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClosedStateSetsMatchTheS1Contract(t *testing.T) {
	t.Parallel()

	require.Equal(t, []CredentialStatus{CredentialActive, CredentialRevoked, CredentialExpired}, CredentialStatuses())
	require.Equal(t, []ProcessState{ProcessUninitialized, ProcessStarting, ProcessReady, ProcessStorageFailed, ProcessDraining}, ProcessStates())
	require.Equal(t, []SQLiteState{SQLiteUninitialized, SQLiteReady, SQLiteLatched}, SQLiteStates())
	require.Equal(t, []KeyringCapability{KeyringReady, KeyringAbsent, KeyringLocked, KeyringInteractionRequired, KeyringUnavailable, KeyringUnsupported}, KeyringCapabilities())
	require.Equal(t, []BackupState{BackupIdle, BackupCreating}, BackupStates())
	require.Equal(t, []InvalidationKind{InvalidationAdminCredentials, InvalidationSystemStatus, InvalidationBackups}, InvalidationKinds())
	require.Equal(t, AgentAuthDenyAll, AgentAuthMode("deny_all"))
}
