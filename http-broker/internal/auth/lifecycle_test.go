package auth

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"
)

const (
	testAgent = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testAdmin = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testThird = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func TestEnsureTokenSetCreatesDistinctCanonicalTokens(t *testing.T) {
	paths := testTokenPaths(t)

	tokens, err := EnsureTokenSet(paths)
	require.NoError(t, err)
	requireValidPair(t, tokens)
	requireTokenFile(t, paths.Agent, tokens.Agent)
	requireTokenFile(t, paths.Admin, tokens.Admin)

	info, err := os.Stat(filepath.Dir(paths.Agent))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o750), info.Mode().Perm())
}

func TestEnsureTokenSetMigratesLegacyTokenToAgent(t *testing.T) {
	paths := testTokenPaths(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(paths.Legacy), 0o750))
	require.NoError(t, os.WriteFile(paths.Legacy, []byte("\n"+testAgent+" \t"), 0o600))

	tokens, err := EnsureTokenSet(paths)
	require.NoError(t, err)
	assertTokenEqual(t, tokens.Agent, testAgent, "migration did not preserve the agent credential")
	assertTokenDifferent(t, tokens.Admin, testAgent, "migration reused the agent credential as admin")
	requireTokenFile(t, paths.Agent, testAgent)
	requireTokenFile(t, paths.Admin, tokens.Admin)
	_, err = os.Stat(paths.Legacy)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestEnsureTokenSetConvergesPartialMigrationWithoutChangingAgent(t *testing.T) {
	paths := testTokenPaths(t)
	writeTestToken(t, paths.Legacy, testAgent)
	writeTestToken(t, paths.Agent, testAgent)

	tokens, err := EnsureTokenSet(paths)
	require.NoError(t, err)
	assertTokenEqual(t, tokens.Agent, testAgent, "partial migration changed the agent credential")
	assertTokenDifferent(t, tokens.Admin, testAgent, "partial migration reused the agent credential as admin")
	_, err = os.Stat(paths.Legacy)
	require.ErrorIs(t, err, os.ErrNotExist)

	again, err := EnsureTokenSet(paths)
	require.NoError(t, err)
	if tokens != again {
		t.Fatal("repeated initialization changed established role credentials")
	}
}

func TestEnsureTokenSetPreservesCanonicalAgentWhenAdminIsMissing(t *testing.T) {
	paths := testTokenPaths(t)
	writeTestToken(t, paths.Agent, testAgent)

	tokens, err := EnsureTokenSet(paths)
	require.NoError(t, err)
	assertTokenEqual(t, tokens.Agent, testAgent, "initialization changed the canonical agent credential")
	assertTokenDifferent(t, tokens.Agent, tokens.Admin, "initialization generated an equal admin credential")
}

func TestEnsureTokenSetPreservesCanonicalAdminAndGeneratesMissingAgent(t *testing.T) {
	paths := testTokenPaths(t)
	writeTestToken(t, paths.Admin, testAdmin)

	tokens, err := EnsureTokenSet(paths)
	require.NoError(t, err)
	assertTokenEqual(t, tokens.Admin, testAdmin, "initialization changed the canonical admin credential")
	assertTokenDifferent(t, tokens.Agent, tokens.Admin, "initialization generated an equal agent credential")
}

func TestEnsureTokenSetUsesLegacyWhenOnlyAdminIsCanonical(t *testing.T) {
	paths := testTokenPaths(t)
	writeTestToken(t, paths.Legacy, testAgent)
	writeTestToken(t, paths.Admin, testAdmin)

	tokens, err := EnsureTokenSet(paths)
	require.NoError(t, err)
	assertTokenSetEqual(t, tokens, TokenSet{Agent: testAgent, Admin: testAdmin}, "legacy-plus-admin migration produced the wrong pair")
	requireTokenFile(t, paths.Agent, testAgent)
}

func TestEnsureTokenSetCanonicalPairWinsOverStaleLegacy(t *testing.T) {
	paths := testTokenPaths(t)
	writeTestToken(t, paths.Agent, testAgent)
	writeTestToken(t, paths.Admin, testAdmin)
	writeTestToken(t, paths.Legacy, "not-a-token")

	tokens, err := EnsureTokenSet(paths)
	require.NoError(t, err)
	assertTokenSetEqual(t, tokens, TokenSet{Agent: testAgent, Admin: testAdmin}, "canonical pair did not win over stale legacy state")
	_, err = os.Stat(paths.Legacy)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestEnsureTokenSetFailsClosedForMalformedOrEqualCanonicalValues(t *testing.T) {
	tests := []struct {
		name  string
		agent string
		admin string
	}{
		{name: "malformed agent", agent: "short", admin: testAdmin},
		{name: "uppercase agent", agent: strings.ToUpper(testAgent), admin: testAdmin},
		{name: "malformed admin", agent: testAgent, admin: "short"},
		{name: "equal", agent: testAgent, admin: testAgent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths := testTokenPaths(t)
			writeTestToken(t, paths.Agent, tt.agent)
			writeTestToken(t, paths.Admin, tt.admin)

			_, err := EnsureTokenSet(paths)
			require.Error(t, err)
			require.NotContains(t, err.Error(), testAgent)
			require.NotContains(t, err.Error(), testAdmin)
		})
	}
}

func TestEnsureTokenSetFailsForRequiredMalformedLegacy(t *testing.T) {
	paths := testTokenPaths(t)
	writeTestToken(t, paths.Legacy, "malformed")
	writeTestToken(t, paths.Admin, testAdmin)

	_, err := EnsureTokenSet(paths)
	require.Error(t, err)
	require.NoFileExists(t, paths.Agent)
}

func TestEnsureTokenSetContextStopsWaitingForContendedLock(t *testing.T) {
	paths := testTokenPaths(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(paths.Lock), 0o750))
	held := flock.New(paths.Lock)
	require.NoError(t, held.Lock())
	t.Cleanup(func() { _ = held.Unlock() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := EnsureTokenSetContext(ctx, paths)

	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestEnsureTokenSetRetriesGeneratedTokenCollisions(t *testing.T) {
	paths := testTokenPaths(t)
	previousRandom := tokenRandom
	tokenRandom = bytes.NewReader(append(append(make([]byte, 32), make([]byte, 32)...), bytes.Repeat([]byte{1}, 32)...))
	t.Cleanup(func() { tokenRandom = previousRandom })

	tokens, err := EnsureTokenSet(paths)
	require.NoError(t, err)
	assertTokenEqual(t, tokens.Agent, strings.Repeat("0", 64), "collision test generated the wrong agent credential")
	assertTokenEqual(t, tokens.Admin, strings.Repeat("01", 32), "collision retry generated the wrong admin credential")
}

func TestEnsureTokenSetConcurrentProcessesConverge(t *testing.T) {
	paths := testTokenPaths(t)
	commands := []*exec.Cmd{
		tokenHelperCommand(t, "ensure", "", paths),
		tokenHelperCommand(t, "ensure", "", paths),
	}
	for _, command := range commands {
		require.NoError(t, command.Start())
	}
	for _, command := range commands {
		require.NoError(t, command.Wait())
	}

	tokens, err := EnsureTokenSet(paths)
	require.NoError(t, err)
	requireValidPair(t, tokens)
}

func TestConcurrentOppositeRoleRotationSerializesValidationAndWrites(t *testing.T) {
	paths := testTokenPaths(t)
	writeTestToken(t, paths.Agent, testAgent)
	writeTestToken(t, paths.Admin, testAdmin)
	commands := []*exec.Cmd{
		tokenHelperCommand(t, "rotate", string(AgentRole), paths),
		tokenHelperCommand(t, "rotate", string(AdminRole), paths),
	}
	for _, command := range commands {
		require.NoError(t, command.Start())
	}
	for _, command := range commands {
		require.NoError(t, command.Wait())
	}

	tokens, err := EnsureTokenSet(paths)
	require.NoError(t, err)
	requireValidPair(t, tokens)
	assertTokenDifferent(t, tokens.Agent, testAgent, "concurrent agent rotation did not change the credential")
	assertTokenDifferent(t, tokens.Admin, testAdmin, "concurrent admin rotation did not change the credential")
}

func TestTokenProcessHelper(t *testing.T) {
	if os.Getenv("MCP_BROKER_TOKEN_HELPER") != "1" {
		return
	}
	paths := TokenPaths{
		Agent:  os.Getenv("MCP_BROKER_AGENT_TOKEN_PATH"),
		Admin:  os.Getenv("MCP_BROKER_ADMIN_TOKEN_PATH"),
		Legacy: os.Getenv("MCP_BROKER_LEGACY_TOKEN_PATH"),
		Lock:   os.Getenv("MCP_BROKER_TOKEN_LOCK_PATH"),
	}
	var err error
	switch os.Getenv("MCP_BROKER_TOKEN_HELPER_ACTION") {
	case "ensure":
		_, err = EnsureTokenSet(paths)
	case "rotate":
		_, err = RotateToken(paths, Role(os.Getenv("MCP_BROKER_TOKEN_HELPER_ROLE")))
	default:
		err = errors.New("unknown helper action")
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestRotateTokenChangesOnlySelectedRoleAndRepairsEquality(t *testing.T) {
	paths := testTokenPaths(t)
	writeTestToken(t, paths.Agent, testAgent)
	writeTestToken(t, paths.Admin, testAdmin)

	rotated, err := RotateToken(paths, AgentRole)
	require.NoError(t, err)
	assertTokenDifferent(t, rotated.Agent, testAgent, "agent rotation did not change the credential")
	assertTokenEqual(t, rotated.Admin, testAdmin, "agent rotation changed the admin credential")
	requireTokenFile(t, paths.Admin, testAdmin)

	writeTestToken(t, paths.Agent, testAdmin)
	repaired, err := RotateToken(paths, AgentRole)
	require.NoError(t, err)
	assertTokenDifferent(t, repaired.Agent, repaired.Admin, "rotation did not repair equal role credentials")
	assertTokenEqual(t, repaired.Admin, testAdmin, "equality repair changed the admin credential")
}

func TestRotateTokenDoesNotChangeCanonicalFileWhenLegacyRetirementFails(t *testing.T) {
	paths := testTokenPaths(t)
	writeTestToken(t, paths.Agent, testAgent)
	writeTestToken(t, paths.Admin, testAdmin)
	require.NoError(t, os.MkdirAll(paths.Legacy, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(paths.Legacy, "blocker"), []byte("x"), 0o600))

	_, err := RotateToken(paths, AgentRole)

	require.Error(t, err)
	requireTokenFile(t, paths.Agent, testAgent)
}

func TestStoreReloadRetainsInvalidRolesAndPublishesSafePair(t *testing.T) {
	tests := []struct {
		name      string
		agentDisk *string
		adminDisk *string
		want      TokenSet
		wantAgent bool
		wantAdmin bool
	}{
		{name: "prior admin cannot become agent", agentDisk: ptr(testAdmin), adminDisk: ptr(testThird), want: TokenSet{Agent: testAgent, Admin: testThird}, wantAgent: true},
		{name: "prior agent cannot become admin", agentDisk: ptr(testThird), adminDisk: ptr(testAgent), want: TokenSet{Agent: testThird, Admin: testAdmin}, wantAdmin: true},
		{name: "opposite role swap", agentDisk: ptr(testAdmin), adminDisk: ptr(testAgent), want: TokenSet{Agent: testAgent, Admin: testAdmin}, wantAgent: true, wantAdmin: true},
		{name: "new equal pair", agentDisk: ptr(testThird), adminDisk: ptr(testThird), want: TokenSet{Agent: testAgent, Admin: testAdmin}, wantAgent: true, wantAdmin: true},
		{name: "malformed agent valid admin", agentDisk: ptr("bad"), adminDisk: ptr(testThird), want: TokenSet{Agent: testAgent, Admin: testThird}, wantAgent: true},
		{name: "missing agent valid admin", agentDisk: nil, adminDisk: ptr(testThird), want: TokenSet{Agent: testAgent, Admin: testThird}, wantAgent: true},
		{name: "valid agent malformed admin", agentDisk: ptr(testThird), adminDisk: ptr("bad"), want: TokenSet{Agent: testThird, Admin: testAdmin}, wantAdmin: true},
		{name: "valid agent missing admin", agentDisk: ptr(testThird), adminDisk: nil, want: TokenSet{Agent: testThird, Admin: testAdmin}, wantAdmin: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths := testTokenPaths(t)
			writeOptionalToken(t, paths.Agent, tt.agentDisk)
			writeOptionalToken(t, paths.Admin, tt.adminDisk)
			store, err := NewStore(TokenSet{Agent: testAgent, Admin: testAdmin})
			require.NoError(t, err)

			result := store.Reload(paths)
			assertTokenSetEqual(t, result.Tokens, tt.want, "reload published the wrong role pair")
			assertTokenSetEqual(t, store.Snapshot(), tt.want, "store snapshot differed from reload result")
			require.Equal(t, tt.wantAgent, result.AgentErr != nil)
			require.Equal(t, tt.wantAdmin, result.AdminErr != nil)
		})
	}
}

func TestStoreReloadRetainsUnreadableRoleWhileApplyingOtherRole(t *testing.T) {
	paths := testTokenPaths(t)
	require.NoError(t, os.MkdirAll(paths.Agent, 0o750))
	writeTestToken(t, paths.Admin, testThird)
	store, err := NewStore(TokenSet{Agent: testAgent, Admin: testAdmin})
	require.NoError(t, err)

	result := store.Reload(paths)

	require.Error(t, result.AgentErr)
	require.NoError(t, result.AdminErr)
	assertTokenSetEqual(t, result.Tokens, TokenSet{Agent: testAgent, Admin: testThird}, "unreadable-role reload published the wrong pair")
}

func TestStoreSnapshotNeverObservesPartialPair(t *testing.T) {
	store, err := NewStore(TokenSet{Agent: testAgent, Admin: testAdmin})
	require.NoError(t, err)
	paths := testTokenPaths(t)

	var wg sync.WaitGroup
	start := make(chan struct{})
	errCh := make(chan error, 1)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 1000 {
				got := store.Snapshot()
				if got != (TokenSet{Agent: testAgent, Admin: testAdmin}) && got != (TokenSet{Agent: testThird, Admin: strings.Repeat("d", 64)}) {
					select {
					case errCh <- errors.New("observed a partial token pair"):
					default:
					}
					return
				}
			}
		}()
	}
	writeTestToken(t, paths.Agent, testThird)
	writeTestToken(t, paths.Admin, strings.Repeat("d", 64))
	close(start)
	for range 1000 {
		result := store.Reload(paths)
		require.NoError(t, result.AgentErr)
		require.NoError(t, result.AdminErr)
		writeTestToken(t, paths.Agent, testAgent)
		writeTestToken(t, paths.Admin, testAdmin)
		result = store.Reload(paths)
		require.NoError(t, result.AgentErr)
		require.NoError(t, result.AdminErr)
		writeTestToken(t, paths.Agent, testThird)
		writeTestToken(t, paths.Admin, strings.Repeat("d", 64))
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
}

func tokenHelperCommand(t *testing.T, action, role string, paths TokenPaths) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestTokenProcessHelper$")
	command.Env = append(os.Environ(),
		"MCP_BROKER_TOKEN_HELPER=1",
		"MCP_BROKER_TOKEN_HELPER_ACTION="+action,
		"MCP_BROKER_TOKEN_HELPER_ROLE="+role,
		"MCP_BROKER_AGENT_TOKEN_PATH="+paths.Agent,
		"MCP_BROKER_ADMIN_TOKEN_PATH="+paths.Admin,
		"MCP_BROKER_LEGACY_TOKEN_PATH="+paths.Legacy,
		"MCP_BROKER_TOKEN_LOCK_PATH="+paths.Lock,
	)
	return command
}

func testTokenPaths(t *testing.T) TokenPaths {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "config")
	return TokenPaths{
		Agent:  filepath.Join(dir, "agent-token"),
		Admin:  filepath.Join(dir, "admin-token"),
		Legacy: filepath.Join(dir, "auth-token"),
		Lock:   filepath.Join(dir, ".token.lock"),
	}
}

func writeTestToken(t *testing.T, path, value string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(value), 0o600))
}

func writeOptionalToken(t *testing.T, path string, value *string) {
	t.Helper()
	if value != nil {
		writeTestToken(t, path, *value)
	}
}

func requireTokenFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	if string(data) != want {
		t.Fatal("token file contents did not match the expected role value")
	}
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func requireValidPair(t *testing.T, tokens TokenSet) {
	t.Helper()
	require.NoError(t, validateToken(tokens.Agent))
	require.NoError(t, validateToken(tokens.Admin))
	if tokens.Agent == tokens.Admin {
		t.Fatal("generated role credentials were equal")
	}
}

func assertTokenEqual(t *testing.T, got, want, message string) {
	t.Helper()
	if got != want {
		t.Fatal(message)
	}
}

func assertTokenDifferent(t *testing.T, left, right, message string) {
	t.Helper()
	if left == right {
		t.Fatal(message)
	}
}

func assertTokenSetEqual(t *testing.T, got, want TokenSet, message string) {
	t.Helper()
	if got != want {
		t.Fatal(message)
	}
}

func ptr(value string) *string { return &value }
