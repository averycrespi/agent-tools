package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
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

// TestMasterKeyRotate_RewrapOnly asserts that master-key rotation touches the
// meta row only — no UPDATE hits the secrets table regardless of how many rows
// it holds. We prove this by snapshotting every row's raw (ciphertext, nonce)
// before rotation, rotating, re-reading, and asserting byte-equality.
//
// Why this matters: before the DEK indirection, rotation was O(rows) — every
// secret had to be decrypted under the old key and re-encrypted under the new.
// With the KEK/DEK split, rotation rewraps the single DEK blob in meta and
// leaves row ciphertexts untouched. This test pins that invariant so a future
// refactor can't silently reintroduce the O(rows) path.
func TestMasterKeyRotate_RewrapOnly(t *testing.T) {
	// 1000 inserts exercises the invariant at scale but is slow under -race; skip
	// in short mode so `go test -short` stays fast.
	if testing.Short() {
		t.Skip("skipping 1000-secret rotation test in short mode")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	const n = 1000
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("secret-%04d", i)
		value := fmt.Sprintf("value-%04d", i)
		require.NoError(t, s.Set(ctx, name, "", value, "", []string{"**"}))
	}

	// Snapshot raw row ciphertexts + nonces before rotation.
	before := snapshotSecretRows(t, db)
	require.Len(t, before, n)

	// Capture meta dek_wrapped so we can sanity-check rotation actually touched
	// meta (otherwise a no-op implementation would trivially pass the row check).
	var metaDEKBefore []byte
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT dek_wrapped FROM meta WHERE key = 'active_key_id'`,
	).Scan(&metaDEKBefore))

	var out bytes.Buffer
	require.NoError(t, execMasterKeyRotate(ctx, s, &out, confirmYes, noSIGHUP))

	after := snapshotSecretRows(t, db)
	require.Len(t, after, n)

	// Every row's ciphertext and nonce must be byte-identical — proves no
	// UPDATE touched the secrets table during rotation.
	for id, beforeRow := range before {
		afterRow, ok := after[id]
		require.True(t, ok, "row %d missing after rotation", id)
		assert.True(t, bytes.Equal(beforeRow.ciphertext, afterRow.ciphertext),
			"row %d ciphertext changed during rotation", id)
		assert.True(t, bytes.Equal(beforeRow.nonce, afterRow.nonce),
			"row %d nonce changed during rotation", id)
	}

	// Sanity: rotation DID update meta (the wrapped DEK blob must differ under
	// a new KEK). Without this, a broken "do nothing" implementation would
	// vacuously satisfy the row-equality checks above.
	var metaDEKAfter []byte
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT dek_wrapped FROM meta WHERE key = 'active_key_id'`,
	).Scan(&metaDEKAfter))
	assert.False(t, bytes.Equal(metaDEKBefore, metaDEKAfter),
		"meta.dek_wrapped must change on master-key rotation")

	// Every secret must still decrypt to its original plaintext under the
	// in-memory store (which now holds the new master key).
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("secret-%04d", i)
		want := fmt.Sprintf("value-%04d", i)
		got, _, _, err := s.Get(ctx, name, "")
		require.NoError(t, err, "get %s", name)
		require.Equal(t, want, got, "decrypted value for %s", name)
	}
}

// rawSecretRow is the subset of a secrets row we compare for rotation
// invariance — if rotation didn't UPDATE the row, these bytes stay identical.
type rawSecretRow struct {
	ciphertext []byte
	nonce      []byte
}

// snapshotSecretRows reads (id, ciphertext, nonce) for every secrets row into
// a map keyed by id. Used to assert rotation leaves row bytes untouched.
func snapshotSecretRows(t *testing.T, db *sql.DB) map[int64]rawSecretRow {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT id, ciphertext, nonce FROM secrets`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	out := make(map[int64]rawSecretRow)
	for rows.Next() {
		var id int64
		var ct, nonce []byte
		require.NoError(t, rows.Scan(&id, &ct, &nonce))
		// Copy the slices — database/sql reuses the underlying buffers across
		// Scan calls, so without the copy we'd end up with every map entry
		// pointing at the same bytes.
		ctCopy := append([]byte(nil), ct...)
		nonceCopy := append([]byte(nil), nonce...)
		out[id] = rawSecretRow{ciphertext: ctCopy, nonce: nonceCopy}
	}
	require.NoError(t, rows.Err())
	return out
}
