package main

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/agents"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agentTestDB opens a temporary SQLite database with all migrations applied.
func agentTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newTestRegistry creates an agents.Registry backed by a fresh test database.
func newTestRegistry(t *testing.T) agents.Registry {
	t.Helper()
	r, err := agents.NewRegistry(context.Background(), agentTestDB(t))
	require.NoError(t, err)
	return r
}

// TestAgentAdd_PrintsTokenOnce verifies that "agent add <name>" prints the
// full token exactly once and a ready-to-paste proxy URL.
func TestAgentAdd_PrintsTokenOnce(t *testing.T) {
	r := newTestRegistry(t)
	var out bytes.Buffer

	err := execAgentAdd(context.Background(), r, "claude", "test agent", "127.0.0.1:8220", &out, noSIGHUP)
	require.NoError(t, err)

	output := out.String()

	// Token must appear exactly once.
	// Collect the token by scanning "token: " line.
	var token string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "token: ") {
			token = strings.TrimPrefix(line, "token: ")
			token = strings.TrimSpace(token)
		}
	}
	require.NotEmpty(t, token, "output must contain a 'token: ...' line")
	assert.True(t, strings.HasPrefix(token, "agw_"), "token must start with agw_")

	// Token must appear in the URL lines (embedded in HTTPS_PROXY / HTTP_PROXY).
	// The full token string will appear multiple times (once per URL line plus the
	// token: line), but it must NOT appear on any other lines.
	assert.Contains(t, output, "HTTPS_PROXY=http://x:"+token+"@127.0.0.1:8220")
	assert.Contains(t, output, "HTTP_PROXY=http://x:"+token+"@127.0.0.1:8220")

	// Every line that contains the token must be one of the three expected lines.
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, token) {
			continue
		}
		isTokenLine := line == "token: "+token
		isHTTPSLine := line == "HTTPS_PROXY=http://x:"+token+"@127.0.0.1:8220"
		isHTTPLine := line == "HTTP_PROXY=http://x:"+token+"@127.0.0.1:8220"
		assert.True(t, isTokenLine || isHTTPSLine || isHTTPLine,
			"unexpected line containing token: %q", line)
	}
}

// TestAgentAdd_TokenAppearsOnceAcrossAllLines checks that the token value
// is not duplicated — it should appear in the URL lines (combined into one
// token-equivalent), not on a separate standalone line AND the URL.
//
// Actually the spec says:
//
//	token: agw_…
//	HTTPS_PROXY=http://x:agw_…@…
//	HTTP_PROXY=http://x:agw_…@…
//
// That means the literal token string appears 3 times in the output.  The
// requirement "prints the token ONCE" means "shows the raw token on its own
// line exactly once" — the URL lines embed the token but that is intentional.
// This test verifies the standalone token line appears exactly once.
func TestAgentAdd_TokenLineAppearsOnce(t *testing.T) {
	r := newTestRegistry(t)
	var out bytes.Buffer

	err := execAgentAdd(context.Background(), r, "bot", "", "127.0.0.1:8220", &out, noSIGHUP)
	require.NoError(t, err)

	output := out.String()
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")

	var tokenLines []string
	for _, l := range lines {
		if strings.HasPrefix(l, "token: ") {
			tokenLines = append(tokenLines, l)
		}
	}
	assert.Len(t, tokenLines, 1, "exactly one 'token: ...' line must be printed")
}

// TestAgentAdd_DuplicateNameMessage verifies that re-adding an existing
// agent returns a user-friendly error that names the agent and suggests
// the `agent rotate` command, rather than leaking a SQLite constraint
// message.
func TestAgentAdd_DuplicateNameMessage(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()
	var out bytes.Buffer

	require.NoError(t, execAgentAdd(ctx, r, "dupe", "", "127.0.0.1:8220", &out, noSIGHUP))

	err := execAgentAdd(ctx, r, "dupe", "", "127.0.0.1:8220", &out, noSIGHUP)
	require.Error(t, err)

	msg := err.Error()
	assert.Contains(t, msg, `"dupe"`, "error should name the duplicated agent")
	assert.Contains(t, msg, "already exists")
	assert.Contains(t, msg, "agent rotate dupe", "error should suggest rotate as the next step")
	assert.NotContains(t, msg, "sqlite", "raw sqlite error must not leak")
}

// TestAgentList_NeverShowsFullToken verifies that "agent list" output
// never contains the full token — only the 12-char prefix is visible.
func TestAgentList_NeverShowsFullToken(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()

	tok, err := r.Add(ctx, "mybot", "list test")
	require.NoError(t, err)

	var out bytes.Buffer
	err = execAgentList(ctx, r, &out)
	require.NoError(t, err)

	output := out.String()
	// The full token must NOT appear.
	assert.NotContains(t, output, tok, "full token must never appear in list output")
	// The prefix (first 12 chars) is acceptable.
	assert.Contains(t, output, agents.Prefix(tok), "token prefix must appear in list output")
	// Name must appear.
	assert.Contains(t, output, "mybot")
}

// TestAgentList_TabularColumns checks that the list header includes expected
// column names.
func TestAgentList_TabularColumns(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()

	_, err := r.Add(ctx, "alpha", "first")
	require.NoError(t, err)
	_, err = r.Add(ctx, "beta", "second")
	require.NoError(t, err)

	var out bytes.Buffer
	err = execAgentList(ctx, r, &out)
	require.NoError(t, err)

	output := out.String()
	assert.Contains(t, output, "NAME")
	assert.Contains(t, output, "PREFIX")
	assert.Contains(t, output, "CREATED")
	assert.Contains(t, output, "alpha")
	assert.Contains(t, output, "beta")
	assert.Contains(t, output, "first")
	assert.Contains(t, output, "second")
}

// TestAgentRotate_InvalidatesPrevious verifies that after "agent rotate <name>"
// the output contains a new token + URL and the old token is invalidated.
func TestAgentRotate_InvalidatesPrevious(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()

	oldTok, err := r.Add(ctx, "spinner", "rotate me")
	require.NoError(t, err)

	var out bytes.Buffer
	err = execAgentRotate(ctx, r, "spinner", "127.0.0.1:8220", &out, confirmYes, noSIGHUP)
	require.NoError(t, err)

	output := out.String()

	// New token must appear in output.
	var newTok string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "token: ") {
			newTok = strings.TrimSpace(strings.TrimPrefix(line, "token: "))
		}
	}
	require.NotEmpty(t, newTok, "rotated output must contain a 'token: ...' line")
	assert.True(t, strings.HasPrefix(newTok, "agw_"), "new token must start with agw_")
	assert.NotEqual(t, oldTok, newTok, "new token must differ from old token")

	// Ready-to-paste URL for new token.
	assert.Contains(t, output, "HTTPS_PROXY=http://x:"+newTok+"@127.0.0.1:8220")

	// Old token must be rejected.
	_, authErr := r.Authenticate(ctx, oldTok)
	assert.ErrorIs(t, authErr, agents.ErrInvalidToken, "old token must be invalid after rotate")

	// New token must authenticate.
	a, authErr := r.Authenticate(ctx, newTok)
	require.NoError(t, authErr)
	assert.Equal(t, "spinner", a.Name)
}

// TestAgentRotate_NotFound verifies that rotate on a non-existent agent
// returns ErrNotFound.
func TestAgentRotate_NotFound(t *testing.T) {
	r := newTestRegistry(t)
	var out bytes.Buffer
	err := execAgentRotate(context.Background(), r, "ghost", "127.0.0.1:8220", &out, confirmYes, noSIGHUP)
	require.Error(t, err)
	assert.ErrorIs(t, err, agents.ErrNotFound)
}

// TestAgentRm_RemovesAgent verifies that "agent rm <name>" removes the agent
// and that the token is subsequently rejected.
func TestAgentRm_RemovesAgent(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()

	tok, err := r.Add(ctx, "doomed", "")
	require.NoError(t, err)

	var out bytes.Buffer
	err = execAgentRm(ctx, r, "doomed", &out, confirmYes, noSIGHUP)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "doomed")

	// Token must be rejected after removal.
	_, authErr := r.Authenticate(ctx, tok)
	assert.ErrorIs(t, authErr, agents.ErrInvalidToken)

	// Agent must no longer appear in list.
	var listOut bytes.Buffer
	require.NoError(t, execAgentList(ctx, r, &listOut))
	assert.NotContains(t, listOut.String(), "doomed")
}

func TestAgentRmCmd_HasLongHelp(t *testing.T) {
	cmd := newAgentRmCmd()
	require.NotEmpty(t, cmd.Long)
	require.Contains(t, cmd.Long, "Immediate consequences")
	require.Contains(t, cmd.Long, "Recovery")
	require.Contains(t, cmd.Long, "407")
}

// TestAgentRm_NotFound verifies that rm on a non-existent agent returns
// ErrNotFound without invoking the confirmation prompt — it's pointless to
// ask "are you sure?" about something that isn't there.
func TestAgentRm_NotFound(t *testing.T) {
	r := newTestRegistry(t)
	var out bytes.Buffer
	confirmCalled := false
	confirm := func() (bool, error) {
		confirmCalled = true
		return true, nil
	}
	err := execAgentRm(context.Background(), r, "ghost", &out, confirm, noSIGHUP)
	require.Error(t, err)
	assert.ErrorIs(t, err, agents.ErrNotFound)
	assert.False(t, confirmCalled, "confirm prompt must not be shown for a non-existent agent")
}
