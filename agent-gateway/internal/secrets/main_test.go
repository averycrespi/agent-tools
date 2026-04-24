package secrets_test

import (
	"os"
	"testing"

	keyring "github.com/zalando/go-keyring"
)

// TestMain stubs the OS keychain with an in-memory mock so tests that exercise
// NewStore / ResolveID are hermetic. Without this, a stale "master-key-1" entry
// in the real macOS Keychain short-circuits ResolveID before the legacy-file
// migration path runs, breaking TestStore_LegacyKeyMigration.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}
