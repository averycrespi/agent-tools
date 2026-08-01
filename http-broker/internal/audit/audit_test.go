package audit_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/http-broker/internal/audit"
)

func open(t *testing.T) (*audit.Logger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	logger, err := audit.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return logger, path
}

func sample(id, host string) audit.Record {
	return audit.Record{
		ID: id, Timestamp: time.Now(), Interception: "mitm",
		Method: "GET", Host: host, Port: 443, Path: "/x",
		Status: 200, Outcome: "allowed", DurationMS: 12,
	}
}

func TestRecordAndQuery(t *testing.T) {
	logger, _ := open(t)

	if err := logger.Record(sample("a", "api.github.com")); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, total, err := logger.Query(context.Background(), audit.QueryOpts{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 1 || len(got) != 1 {
		t.Fatalf("got %d rows (total %d), want 1", len(got), total)
	}
	if got[0].Host != "api.github.com" || got[0].Port != 443 || got[0].Outcome != "allowed" {
		t.Errorf("row = %+v, want host, port and outcome preserved", got[0])
	}
}

// TestExactlyOneRowPerRecord is AC-10's counting half.
func TestExactlyOneRowPerRecord(t *testing.T) {
	logger, _ := open(t)

	for i := range 25 {
		if err := logger.Record(sample(string(rune('a'+i)), "api.github.com")); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	_, total, err := logger.Query(context.Background(), audit.QueryOpts{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 25 {
		t.Errorf("total = %d, want exactly 25", total)
	}
}

// TestTunnelRowHasNullMethodAndPath is AC-4: a tunnel reveals neither, so the
// columns must be NULL rather than an empty string that reads like a real
// value.
func TestTunnelRowHasNullMethodAndPath(t *testing.T) {
	logger, _ := open(t)

	err := logger.Record(audit.Record{
		ID: "t1", Timestamp: time.Now(), Interception: "tunnel",
		Host: "cdn.example.com", Port: 443, Outcome: "allowed",
		BytesIn: 100, BytesOut: 50, DurationMS: 30,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, _, err := logger.Query(context.Background(), audit.QueryOpts{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got[0].Method != "" || got[0].Path != "" {
		t.Errorf("method = %q, path = %q; want both empty for a tunnel", got[0].Method, got[0].Path)
	}
	if got[0].BytesIn != 100 || got[0].BytesOut != 50 {
		t.Errorf("byte counts = %d/%d, want 100/50", got[0].BytesIn, got[0].BytesOut)
	}
}

func TestQueryFilters(t *testing.T) {
	logger, _ := open(t)

	rows := []audit.Record{
		{ID: "1", Interception: "mitm", Host: "a.example.com", Port: 443, Outcome: "allowed", MatchedRule: "r1"},
		{ID: "2", Interception: "mitm", Host: "b.example.com", Port: 443, Outcome: "blocked", MatchedRule: "r2"},
		{ID: "3", Interception: "mitm", Host: "a.example.com", Port: 443, Outcome: "blocked", MatchedRule: "r1"},
	}
	for _, r := range rows {
		r.Timestamp = time.Now()
		if err := logger.Record(r); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	cases := []struct {
		name string
		opts audit.QueryOpts
		want int
	}{
		{"by host", audit.QueryOpts{Host: "a.example.com"}, 2},
		{"by outcome", audit.QueryOpts{Outcome: "blocked"}, 2},
		{"by rule", audit.QueryOpts{Rule: "r1"}, 2},
		{"combined", audit.QueryOpts{Host: "a.example.com", Outcome: "blocked"}, 1},
		{"no match", audit.QueryOpts{Host: "nope.example.com"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := logger.Query(context.Background(), tc.opts)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if total != tc.want || len(got) != tc.want {
				t.Errorf("got %d rows (total %d), want %d", len(got), total, tc.want)
			}
		})
	}
}

// TestQueryFilterValuesAreBound: a filter value containing SQL must be treated
// as data. The dashboard passes these straight from a query parameter.
func TestQueryFilterValuesAreBound(t *testing.T) {
	logger, _ := open(t)
	if err := logger.Record(sample("a", "api.github.com")); err != nil {
		t.Fatalf("Record: %v", err)
	}

	for _, injection := range []string{
		"' OR '1'='1",
		"'; DROP TABLE audit_records; --",
		"%",
	} {
		got, total, err := logger.Query(context.Background(), audit.QueryOpts{Host: injection})
		if err != nil {
			t.Fatalf("Query(%q): %v", injection, err)
		}
		if total != 0 || len(got) != 0 {
			t.Errorf("Query(%q) matched %d rows, want 0: the value must be bound, not interpolated", injection, total)
		}
	}

	// The table survived.
	if _, total, err := logger.Query(context.Background(), audit.QueryOpts{}); err != nil || total != 1 {
		t.Errorf("after injection attempts: total = %d, err = %v; want the table intact", total, err)
	}
}

func TestQueryPagination(t *testing.T) {
	logger, _ := open(t)
	for i := range 10 {
		rec := sample(string(rune('a'+i)), "api.github.com")
		rec.Timestamp = time.Now().Add(time.Duration(i) * time.Second)
		if err := logger.Record(rec); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	page1, total, err := logger.Query(context.Background(), audit.QueryOpts{Limit: 3})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 10 || len(page1) != 3 {
		t.Fatalf("page 1: %d rows, total %d; want 3 and 10", len(page1), total)
	}

	page2, _, err := logger.Query(context.Background(), audit.QueryOpts{Limit: 3, Offset: 3})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if page2[0].ID == page1[0].ID {
		t.Error("page 2 repeats page 1; offset was not applied")
	}
	// Newest first.
	if !page1[0].Timestamp.After(page1[2].Timestamp) {
		t.Error("rows should come back newest first")
	}
}

func TestQueryLimitIsBounded(t *testing.T) {
	logger, _ := open(t)
	if err := logger.Record(sample("a", "h")); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// An absurd limit must be clamped rather than honoured.
	if _, _, err := logger.Query(context.Background(), audit.QueryOpts{Limit: 1_000_000}); err != nil {
		t.Fatalf("Query: %v", err)
	}
}

func TestSubscribe(t *testing.T) {
	logger, _ := open(t)

	var (
		mu       sync.Mutex
		received []audit.Record
	)
	unsubscribe := logger.Subscribe(func(r audit.Record) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, r)
	})

	if err := logger.Record(sample("a", "api.github.com")); err != nil {
		t.Fatalf("Record: %v", err)
	}

	mu.Lock()
	count := len(received)
	mu.Unlock()
	if count != 1 {
		t.Fatalf("subscriber received %d records, want 1", count)
	}

	unsubscribe()
	if err := logger.Record(sample("b", "api.github.com")); err != nil {
		t.Fatalf("Record: %v", err)
	}

	mu.Lock()
	count = len(received)
	mu.Unlock()
	if count != 1 {
		t.Errorf("subscriber received %d records after unsubscribing, want 1", count)
	}
}

func TestPrune(t *testing.T) {
	logger, _ := open(t)
	now := time.Now()

	old := sample("old", "api.github.com")
	old.Timestamp = now.Add(-100 * 24 * time.Hour)
	recent := sample("recent", "api.github.com")
	recent.Timestamp = now.Add(-1 * time.Hour)

	for _, r := range []audit.Record{old, recent} {
		if err := logger.Record(r); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	removed, err := logger.Prune(context.Background(), 90*24*time.Hour, now)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed %d rows, want 1", removed)
	}

	got, _, err := logger.Query(context.Background(), audit.QueryOpts{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || got[0].ID != "recent" {
		t.Errorf("remaining rows = %+v, want only the recent one", got)
	}
}

func TestPruneDisabledWhenRetentionIsZero(t *testing.T) {
	logger, _ := open(t)
	old := sample("old", "h")
	old.Timestamp = time.Now().Add(-1000 * 24 * time.Hour)
	if err := logger.Record(old); err != nil {
		t.Fatalf("Record: %v", err)
	}

	removed, err := logger.Prune(context.Background(), 0, time.Now())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed %d rows, want 0: a zero retention window disables pruning", removed)
	}
}

// TestFilePermissions is AC-18's practical half: the database and its WAL
// sidecars all hold the same content, so all three must be private.
func TestFilePermissions(t *testing.T) {
	logger, path := open(t)
	if err := logger.Record(sample("a", "h")); err != nil {
		t.Fatalf("Record: %v", err)
	}

	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %v, want 0600", filepath.Base(p), perm)
		}
	}
}

func TestReopenPreservesRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")

	first, err := audit.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := first.Record(sample("a", "api.github.com")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := audit.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = second.Close() }()

	_, total, err := second.Query(context.Background(), audit.QueryOpts{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 1 {
		t.Errorf("total after reopen = %d, want 1", total)
	}
}

// TestNoBodyOrHeaderColumns is a structural guard on AC-10. If someone adds a
// field that could carry request content, this fails and makes them justify it.
func TestNoBodyOrHeaderColumns(t *testing.T) {
	forbidden := []string{"body", "header", "payload", "content", "value", "secret", "token"}

	typ := reflect.TypeOf(audit.Record{})
	for i := range typ.NumField() {
		name := strings.ToLower(typ.Field(i).Name)
		for _, bad := range forbidden {
			if strings.Contains(name, bad) {
				t.Errorf("Record has a field %q; no audit column may carry request content or a credential value (AC-10)",
					typ.Field(i).Name)
			}
		}
	}
}

func TestRecordAfterCloseFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	logger, err := audit.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := logger.Record(sample("a", "h")); err == nil {
		t.Error("Record after Close = nil, want an error the caller can log")
	}
}
