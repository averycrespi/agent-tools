package main

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"testing"
)

func testStack(token string) *stack {
	return &stack{
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		token: token,
	}
}

var testDashAddr = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8221}

// TestDashboardURLCarriesTheToken pins the shape the dashboard's ?token=
// exchange depends on: the token is a query parameter on the bound address, so
// requireAuth can swap it for a cookie and redirect it out of the URL.
func TestDashboardURLCarriesTheToken(t *testing.T) {
	got := dashboardURL(testDashAddr, "deadbeef")
	if want := "http://127.0.0.1:8221/?token=deadbeef"; got != want {
		t.Fatalf("dashboardURL = %q, want %q", got, want)
	}

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parsing %q: %v", got, err)
	}
	if parsed.Path != "/" {
		t.Errorf("path = %q, want %q — the redirect allowlist only covers known routes", parsed.Path, "/")
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
		announceDashboard(testStack("deadbeef"), testDashAddr, &buf, open)
		if buf.Len() != 0 {
			t.Errorf("open=%v: wrote %q, want nothing", open, buf.String())
		}
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
