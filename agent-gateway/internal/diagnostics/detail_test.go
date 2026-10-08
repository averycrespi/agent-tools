package diagnostics

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalDetailPreservesCausesWithoutSecrets(t *testing.T) {
	secret := "actual-upstream-credential-123"
	err := fmt.Errorf("connect inventory.example: %w", errors.Join(errors.New("TLS certificate expired"), errors.New("cleanup: permission denied Authorization: Bearer "+secret)))
	d := Snapshot("remote", "exchange", "inventory.example:443", err, secret)
	require.Contains(t, d.Explanation, "TLS certificate expired")
	require.Contains(t, d.Explanation, "permission denied")
	require.NotContains(t, d.Explanation, secret)
	public := errors.New("proxy connection failed")
	local := WithDetail(public, d)
	require.Equal(t, public.Error(), local.Error())
	require.ErrorIs(t, local, public)
	require.Equal(t, d.Explanation, Snapshot("proxy", "exchange", "inventory.example:443", local).Explanation)
}

func TestLocalDetailBoundsAndEscapes(t *testing.T) {
	d := Snapshot("filesystem", "open", "/tmp/benign.sqlite", errors.New("permission denied\n\x1b[31m"+strings.Repeat("x", 100000)))
	require.Contains(t, d.Explanation, "permission denied")
	require.Contains(t, d.Explanation, "truncated")
	require.NotContains(t, d.Explanation, "\n")
	require.NotContains(t, d.Explanation, "\x1b")
	require.LessOrEqual(t, len(d.Explanation), 640)
	require.Equal(t, "/tmp/benign.sqlite", d.Resource)
}

type cyclicDetailError struct{}

func (*cyclicDetailError) Error() string   { panic("must not format cyclic error") }
func (e *cyclicDetailError) Unwrap() error { return e }
func TestLocalDetailBoundsCauseTraversal(t *testing.T) {
	require.Contains(t, Snapshot("test", "cycle", "", &cyclicDetailError{}).Explanation, "traversal truncated")
}

func TestLocalDetailWorstCaseStillEmitsBoundedRecord(t *testing.T) {
	var sink bytes.Buffer
	adapter := New(&sink, Warn)
	value := strings.Repeat("\x1b<>\\\"", 10000)
	adapter.Observe(Facts{Event: OperatorFailure, Detail: Detail{Component: value, Operation: value, Resource: value, Explanation: value, Native: value, Effect: value, Excerpt: value, Stack: value}})
	require.True(t, adapter.Finish(nil))
	require.NotEmpty(t, sink.Bytes())
	require.LessOrEqual(t, sink.Len(), RecordBytes)
	require.NotContains(t, sink.String(), "\x1b")
	require.Contains(t, sink.String(), "truncated")
}

func TestLocalDetailJoinedLocalAndCleanupSurvive(t *testing.T) {
	first := WithDetail(errors.New("public connection failure"), Snapshot("remote", "dial", "inventory.example", errors.New("DNS no such host")))
	second := WithDetail(errors.New("public cleanup failure"), Snapshot("remote", "close", "socket", errors.New("native descriptor failure")))
	d := Snapshot("proxy", "exchange", "", errors.Join(first, second))
	require.Contains(t, d.Explanation, "DNS no such host")
	require.Contains(t, d.Explanation, "native descriptor failure")
}

func TestLocalDetailDefaultOutputAndDistinctTraffic(t *testing.T) {
	var sink bytes.Buffer
	adapter := New(&sink, Warn)
	for _, why := range []string{"permission denied", "disk full"} {
		f := TrafficFacts(false, "unknown", "opening", "not_started", 0)
		f.Detail = Snapshot("traffic", "open", "/tmp/traffic.sqlite", errors.New(why))
		adapter.Traffic(f)
	}
	require.True(t, adapter.Finish(nil))
	require.Contains(t, sink.String(), "permission denied")
	require.Contains(t, sink.String(), "disk full")
	require.Contains(t, sink.String(), `"schema_version":2`)
}
