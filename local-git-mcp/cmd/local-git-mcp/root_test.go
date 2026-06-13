package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestWarnIfAllowAllPaths_LogsProminentWarning(t *testing.T) {
	var buf bytes.Buffer
	restoreDefaultLogger(t, &buf)

	warnIfAllowAllPaths(true)

	got := buf.String()
	if !strings.Contains(got, "level=WARN") {
		t.Fatalf("expected warning level log, got %q", got)
	}
	if !strings.Contains(got, allowAllPathsWarning) {
		t.Fatalf("expected allow-all-paths warning, got %q", got)
	}
	if !strings.Contains(got, "flag=--allow-all-paths") {
		t.Fatalf("expected flag attribute, got %q", got)
	}
}

func TestWarnIfAllowAllPaths_SilentWhenDisabled(t *testing.T) {
	var buf bytes.Buffer
	restoreDefaultLogger(t, &buf)

	warnIfAllowAllPaths(false)

	if got := buf.String(); got != "" {
		t.Fatalf("expected no log output, got %q", got)
	}
}

func restoreDefaultLogger(t *testing.T, buf *bytes.Buffer) {
	t.Helper()
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
}
