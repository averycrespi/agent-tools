package composition

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigurationTargetMigrationSourceExceptionIsExact(t *testing.T) {
	for _, test := range []struct {
		name, file, sql string
		want            int
	}{
		{"retained FK", "027_configuration_targets.sql", `CREATE TABLE git_grants (principal_id TEXT NOT NULL REFERENCES principals(id));`, 0},
		{"other migration", "028_unrelated.sql", `CREATE TABLE git_grants (principal_id TEXT NOT NULL REFERENCES principals(id));`, 1},
		{"extra principal SQL", "027_configuration_targets.sql", `CREATE TABLE git_grants (principal_id TEXT NOT NULL REFERENCES principals(id)); DELETE FROM principals;`, 1},
		{"other authority SQL", "027_configuration_targets.sql", `CREATE TABLE git_grants (principal_id TEXT NOT NULL REFERENCES principals(id)); UPDATE authorization_meta SET revision=0;`, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "internal/storage/migrations")
			require.NoError(t, os.MkdirAll(directory, 0700))
			original, err := os.ReadFile(filepath.Join(gatewayModuleRoot(t), "internal/storage/migrations/008_authorization.sql"))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(directory, "008_authorization.sql"), original, 0600))
			require.NoError(t, os.WriteFile(filepath.Join(directory, test.file), []byte(test.sql), 0600))
			violations := s3MigrationViolations(t, root)
			require.Len(t, violations, test.want)
		})
	}
}
