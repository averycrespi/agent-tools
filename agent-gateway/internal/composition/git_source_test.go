package composition

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetiredRecoveryHasNoAuthoritySQLExemption(t *testing.T) {
	for _, test := range []struct {
		name, source string
		allowed      bool
	}{
		{"retired stopped fence", "package storage\nfunc restoreKeyringAuthorityFence() { _ = `UPDATE authorization_meta SET revision=revision+1 WHERE singleton=1` }", false},
		{"wrong recovery function", "package storage\nfunc other() { _ = `UPDATE authorization_meta SET revision=revision+1 WHERE singleton=1` }", false},
		{"principal read in fence", "package storage\nfunc restoreKeyringAuthorityFence() { _ = `SELECT id FROM principals` }", false},
		{"grant mutation in fence", "package storage\nfunc restoreKeyringAuthorityFence() { _ = `DELETE FROM grants` }", false},
		{"different authority mutation", "package storage\nfunc restoreKeyringAuthorityFence() { _ = `UPDATE authorization_meta SET revision=0 WHERE singleton=1` }", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := parseProductionFixture(t, "internal/storage/recovery.go", test.source)
			violations := s3SQLViolations(source)
			require.Equal(t, test.allowed, len(violations) == 0, violations)
		})
	}
}
