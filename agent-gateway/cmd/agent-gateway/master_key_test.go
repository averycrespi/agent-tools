package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMasterKeyRotate verifies that "master-key rotate" calls MasterRotate on
// the secrets store, prints a success line, and leaves existing secrets
// decryptable under the new master key.
func TestMasterKeyRotate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "", "val", "", []string{"**"}))

	var out bytes.Buffer
	err := execMasterKeyRotate(ctx, s, &out, confirmYes, noSIGHUP)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "rotated master key")

	// Secret should still decrypt correctly.
	val, _, _, getErr := s.Get(ctx, "tok", "")
	require.NoError(t, getErr)
	assert.Equal(t, "val", val)
}
