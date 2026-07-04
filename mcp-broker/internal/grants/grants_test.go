package grants

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

func TestParseRulesFileAcceptsArrayAndWrappedRules(t *testing.T) {
	arrayRules, err := ParseRulesFile([]byte(`[{
		"tool":"github.*",
		"verdict":"allow",
		"reason":"temporary",
		"args":[{"path":"repo","match":"agent-tools"}]
	}]`))
	require.NoError(t, err)
	require.Len(t, arrayRules, 1)
	require.Equal(t, "temporary", arrayRules[0].Reason)
	require.Len(t, arrayRules[0].Args, 1)

	wrappedRules, err := ParseRulesFile([]byte(`{"rules":[{"tool":"git.push","verdict":"deny"}]}`))
	require.NoError(t, err)
	require.Equal(t, []config.RuleConfig{{Tool: "git.push", Verdict: "deny"}}, wrappedRules)
}

func TestParseRulesFileValidatesArgumentMatchers(t *testing.T) {
	_, err := ParseRulesFile([]byte(`[{"tool":"*","verdict":"allow","args":[{"path":"bad..path","match":"x"}]}]`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "compile grant rules")
}

func TestStoreMintListValidateAndRevoke(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

	minted, err := store.Mint(context.Background(), MintOptions{
		Name:        "deployment",
		Description: "release window",
		TTL:         time.Hour,
		MaxTTL:      24 * time.Hour,
		Now:         now,
		Rules:       []config.RuleConfig{{Tool: "github.*", Verdict: "allow", Reason: "release"}},
	})
	require.NoError(t, err)
	require.Len(t, minted.Token, tokenBytes*2)
	require.NotContains(t, minted.Grant.Fingerprint, minted.Token)
	require.Equal(t, "active", minted.Grant.Status)

	valid, err := store.ValidateToken(context.Background(), minted.Token, now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, minted.Grant.ID, valid.ID)
	require.Equal(t, "deployment", valid.Name)
	require.Equal(t, "active", valid.Status)
	_, err = valid.Engine()
	require.NoError(t, err)

	listed, err := store.List(context.Background(), now.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, minted.Grant.Fingerprint, listed[0].Fingerprint)
	require.Equal(t, "active", listed[0].Status)

	revoked, err := store.Revoke(context.Background(), minted.Grant.Fingerprint[:8], now.Add(2*time.Minute))
	require.NoError(t, err)
	require.Equal(t, "revoked", revoked.Status)
	require.NotNil(t, revoked.RevokedAt)

	revokedAgain, err := store.Revoke(context.Background(), minted.Grant.ID, now.Add(3*time.Minute))
	require.NoError(t, err)
	require.Equal(t, revoked.RevokedAt, revokedAgain.RevokedAt)

	_, err = store.ValidateToken(context.Background(), minted.Token, now.Add(4*time.Minute))
	require.ErrorIs(t, err, ErrRevoked)
}

func TestStoreDoesNotPersistRawToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grants.db")
	store, err := Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	minted, err := store.Mint(context.Background(), MintOptions{
		Name:   "secret",
		TTL:    time.Hour,
		MaxTTL: time.Hour,
		Rules:  []config.RuleConfig{{Tool: "*", Verdict: "allow"}},
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	var tokenHash, fingerprint string
	err = db.QueryRow(`SELECT token_hash, fingerprint FROM grants WHERE id = ?`, minted.Grant.ID).Scan(&tokenHash, &fingerprint)
	require.NoError(t, err)
	require.NotEqual(t, minted.Token, tokenHash)
	require.NotEqual(t, minted.Token, fingerprint)
	require.Len(t, tokenHash, 64)
	require.Len(t, fingerprint, fingerprintChars)

	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(contents), minted.Token)

	data, err := json.Marshal(minted.Grant)
	require.NoError(t, err)
	require.NotContains(t, string(data), minted.Token)
}

func TestStoreMintRejectsInvalidTTLAndMaxTTL(t *testing.T) {
	store := openTestStore(t)
	base := MintOptions{Name: "ops", Rules: []config.RuleConfig{{Tool: "*", Verdict: "allow"}}}

	_, err := store.Mint(context.Background(), base)
	require.ErrorContains(t, err, "ttl must be positive")

	tooLong := base
	tooLong.TTL = 2 * time.Hour
	tooLong.MaxTTL = time.Hour
	_, err = store.Mint(context.Background(), tooLong)
	require.ErrorContains(t, err, "exceeds maximum")

	unnamed := base
	unnamed.Name = "  "
	unnamed.TTL = time.Hour
	unnamed.MaxTTL = time.Hour
	_, err = store.Mint(context.Background(), unnamed)
	require.ErrorContains(t, err, "name is required")
}

func TestStoreValidateTokenRejectsUnknownExpiredAndMalformed(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	minted, err := store.Mint(context.Background(), MintOptions{
		Name:   "short",
		TTL:    time.Minute,
		MaxTTL: time.Hour,
		Now:    now,
		Rules:  []config.RuleConfig{{Tool: "*", Verdict: "allow"}},
	})
	require.NoError(t, err)

	_, err = store.ValidateToken(context.Background(), "not-present", now)
	require.ErrorIs(t, err, ErrUnknown)

	_, err = store.ValidateToken(context.Background(), minted.Token+",other", now)
	require.ErrorIs(t, err, ErrUnknown)

	_, err = store.ValidateToken(context.Background(), minted.Token, now.Add(time.Minute))
	require.ErrorIs(t, err, ErrExpired)
}

func TestNewStoreCreatesPrivateSQLiteFiles(t *testing.T) {
	oldUmask := syscall.Umask(0)
	defer syscall.Umask(oldUmask)

	path := filepath.Join(t.TempDir(), "grants.db")
	store, err := Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	_, err = store.Mint(context.Background(), MintOptions{Name: "ops", TTL: time.Hour, Rules: []config.RuleConfig{{Tool: "*", Verdict: "allow"}}})
	require.NoError(t, err)

	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		info, statErr := os.Stat(file)
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), file)
	}
}

func TestRevokeUnknownReturnsSentinel(t *testing.T) {
	store := openTestStore(t)
	_, err := store.Revoke(context.Background(), "missing", time.Now())
	require.True(t, errors.Is(err, ErrUnknown))
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "grants.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}
