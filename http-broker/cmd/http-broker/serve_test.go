package main

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"

	"github.com/averycrespi/agent-tools/http-broker/internal/auth"
	"github.com/averycrespi/agent-tools/http-broker/internal/dashboard"
)

func testStack(t *testing.T) *stack {
	t.Helper()
	store, err := auth.NewStore(auth.TokenSet{Agent: strings.Repeat("a", 64), Admin: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return &stack{
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		auth: store,
	}
}

var testDashAddr = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8221}

// TestDashboardURLCarriesTheToken pins the shape the dashboard's ?token=
// exchange depends on: the token is a query parameter on the bound address, so
// requireAuth can swap it for a cookie and redirect it out of the URL.
func TestDashboardURLCarriesTheToken(t *testing.T) {
	got := dashboardURL(testDashAddr, "deadbeef")
	if want := "http://127.0.0.1:8221/dashboard/?token=deadbeef"; got != want {
		t.Fatalf("dashboardURL = %q, want %q", got, want)
	}

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parsing %q: %v", got, err)
	}
	if parsed.Path != dashboard.Prefix {
		t.Errorf("path = %q, want %q — the redirect allowlist only covers known routes", parsed.Path, dashboard.Prefix)
	}
	if parsed.Query().Get("token") != "deadbeef" {
		t.Errorf("token query = %q, want %q", parsed.Query().Get("token"), "deadbeef")
	}
}

// TestDashboardURLEscapesTheToken guards the construction rather than today's
// token alphabet. Tokens are hex now, but a token file is operator-editable.
func TestDashboardURLEscapesTheToken(t *testing.T) {
	got := dashboardURL(testDashAddr, "a b&c=d")
	if strings.Contains(got, "a b&c=d") {
		t.Fatalf("dashboardURL = %q, want the token percent-encoded", got)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parsing %q: %v", got, err)
	}
	if parsed.Query().Get("token") != "a b&c=d" {
		t.Errorf("token round-trip = %q, want %q", parsed.Query().Get("token"), "a b&c=d")
	}
}

// TestAnnounceDashboardStaysQuietWhenNotATerminal is the guard that keeps the
// token out of launchd's log file. A non-terminal stdout means the process is
// supervised, where printing the token would persist it and opening a browser
// would be wrong. An empty buffer also proves no browser launch was attempted,
// since the print happens first.
func TestAnnounceDashboardStaysQuietWhenNotATerminal(t *testing.T) {
	for _, open := range []bool{true, false} {
		var buf bytes.Buffer
		opened := false
		announceDashboardWith(testStack(t), testDashAddr, &buf, open, isInteractiveOutput, func(string) error {
			opened = true
			return nil
		})
		if buf.Len() != 0 {
			t.Errorf("open=%v: wrote output, want nothing", open)
		}
		if opened {
			t.Errorf("open=%v: launched a browser for non-terminal output", open)
		}
	}
}

func TestAnnounceDashboardUsesAdminCredentialOnInteractivePTY(t *testing.T) {
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	t.Cleanup(func() {
		_ = master.Close()
		_ = terminal.Close()
	})
	if !isInteractiveOutput(terminal) {
		t.Fatal("PTY was not recognized as interactive output")
	}
	st := testStack(t)
	opened := ""
	announceDashboardWith(st, testDashAddr, terminal, true, isInteractiveOutput, func(target string) error {
		opened = target
		return nil
	})
	buffer := make([]byte, 512)
	count, err := master.Read(buffer)
	if err != nil {
		t.Fatalf("read pty: %v", err)
	}
	output := string(buffer[:count])
	admin := st.auth.Snapshot().Admin
	if !strings.Contains(output, admin) {
		t.Fatal("interactive dashboard URL did not contain the admin credential")
	}
	if strings.Contains(output, st.auth.Snapshot().Agent) {
		t.Fatal("interactive dashboard URL contained the agent credential")
	}
	if strings.TrimPrefix(strings.TrimSpace(output), "Dashboard: ") != opened {
		t.Fatal("printed and opened dashboard URLs differed")
	}
}

func TestReloadAttemptsRoleCredentialsBeforeInvalidConfigReturns(t *testing.T) {
	dir := t.TempDir()
	paths := auth.TokenPaths{
		Agent:  filepath.Join(dir, "agent-token"),
		Admin:  filepath.Join(dir, "admin-token"),
		Legacy: filepath.Join(dir, "auth-token"),
		Lock:   filepath.Join(dir, ".token.lock"),
	}
	old := auth.TokenSet{Agent: strings.Repeat("a", 64), Admin: strings.Repeat("b", 64)}
	store, err := auth.NewStore(old)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	newAgent := strings.Repeat("c", 64)
	if err := os.WriteFile(paths.Agent, []byte(newAgent), 0o600); err != nil {
		t.Fatalf("write agent token: %v", err)
	}
	if err := os.WriteFile(paths.Admin, []byte(old.Admin), 0o600); err != nil {
		t.Fatalf("write admin token: %v", err)
	}
	st := &stack{log: slog.New(slog.NewTextHandler(io.Discard, nil)), auth: store, tokenPaths: paths}
	previousConfig := cfgFile
	cfgFile = filepath.Join(dir, "config-is-a-directory")
	if err := os.Mkdir(cfgFile, 0o750); err != nil {
		t.Fatalf("create invalid config path: %v", err)
	}
	t.Cleanup(func() { cfgFile = previousConfig })

	st.reload()

	got := store.Snapshot()
	if got.Agent != newAgent || got.Admin != old.Admin {
		t.Fatal("valid agent change did not apply before unrelated config reload failure")
	}
}

// TestServeNoOpenFlag pins the flag `serve` advertises, since the launchd
// example and docs both name it.
func TestServeNoOpenFlag(t *testing.T) {
	flag := serveCmd.Flags().Lookup("no-open")
	if flag == nil {
		t.Fatal("serve has no --no-open flag")
	}
	if flag.DefValue != "false" {
		t.Errorf("--no-open default = %q, want %q: the dashboard opens unless asked not to", flag.DefValue, "false")
	}
}
