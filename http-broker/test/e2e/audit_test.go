//go:build e2e

package e2e_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// auditRows reads every audit row as a flat list of column values, so a test
// can sweep for a sentinel without naming each column.
func auditRows(t *testing.T, s *stack) []string {
	t.Helper()

	path := filepath.Join(s.dataDir, "audit.db")
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("opening the audit database: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query(`SELECT id, interception, method, host, port, path, query,
		status, matched_rule, mode, injection, credential_ref, outcome, error
		FROM audit_records ORDER BY ts`)
	if err != nil {
		t.Fatalf("querying the audit database: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		vals := make([]any, 14)
		ptrs := make([]any, 14)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scanning: %v", err)
		}
		parts := make([]string, 0, len(vals))
		for _, v := range vals {
			parts = append(parts, fmt.Sprintf("%v", v))
		}
		out = append(out, strings.Join(parts, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading rows: %v", err)
	}
	return out
}

// waitForRows polls until the expected number of audit rows appears. Auditing
// is off the request path, so a row can land just after the response.
func waitForRows(t *testing.T, s *stack, want int) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var rows []string
	for time.Now().Before(deadline) {
		rows = auditRows(t, s)
		if len(rows) >= want {
			return rows
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected %d audit rows, got %d:\n%s", want, len(rows), strings.Join(rows, "\n"))
	return nil
}

// TestAuditOneRowPerRequest is AC-10's counting half against the real binary.
func TestAuditOneRowPerRequest(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules:      rulesDoc("deny", rule{Name: "up", Host: host, Ports: []int{port}, Mode: "intercept"}),
		AllowAddrs: []string{hostPort(host, port)},
		UpstreamCA: up.CertPEM(t),
	})

	const requests = 3
	for range requests {
		resp, _ := requestThroughProxy(t, s.client(), http.MethodGet,
			fmt.Sprintf("https://%s/thing", hostPort(host, port)))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 (logs:\n%s)", resp.StatusCode, s.Logs())
		}
	}

	rows := waitForRows(t, s, requests)
	if len(rows) != requests {
		t.Errorf("got %d audit rows, want exactly %d:\n%s", len(rows), requests, strings.Join(rows, "\n"))
	}
	for _, row := range rows {
		if !strings.Contains(row, host) {
			t.Errorf("row %q should carry the host", row)
		}
		if !strings.Contains(row, "allowed") {
			t.Errorf("row %q should carry the outcome", row)
		}
	}
}

// TestAuditRecordsRefusedRequests covers the rows AC-10 was missing.
//
// A 407 and a malformed CONNECT target both returned without writing anything,
// so repeated authentication failures — how a misprovisioned sandbox and
// something probing the port both look — left no trace at all.
func TestAuditRecordsRefusedRequests(t *testing.T) {
	s := startStack(t, stackOptions{Rules: rulesDoc("deny")})

	// Unauthenticated CONNECT.
	unauth := dialProxy(t, s)
	writeRaw(t, unauth, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	if resp, err := readResponse(unauth); err != nil {
		t.Fatalf("reading the response: %v", err)
	} else if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("status = %d, want 407", resp.StatusCode)
	}

	// Authenticated CONNECT to a target that cannot be parsed.
	bad := dialProxy(t, s)
	writeRaw(t, bad, fmt.Sprintf("CONNECT not-a-target HTTP/1.1\r\nHost: not-a-target\r\nProxy-Authorization: %s\r\n\r\n",
		basicCredential(s.token)))
	if resp, err := readResponse(bad); err != nil {
		t.Fatalf("reading the response: %v", err)
	} else if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}

	rows := waitForRows(t, s, 2)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "proxy authentication failed") {
		t.Errorf("no row for the refused authentication:\n%s", joined)
	}
	if !strings.Contains(joined, "malformed CONNECT target") {
		t.Errorf("no row for the malformed target:\n%s", joined)
	}
	// Nothing an unauthenticated caller chose may be stored as a host.
	for _, row := range rows {
		if strings.Contains(row, "example.com") || strings.Contains(row, "not-a-target") {
			t.Errorf("row %q records a target that never passed validation", row)
		}
	}
}

// TestAuditRecordsChunkedBodySize pins the byte count for a body whose length
// Go reports as -1, which used to land in the column as negative bytes.
func TestAuditRecordsChunkedBodySize(t *testing.T) {
	up := newUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny", rule{
			Name: "up", Host: host, Ports: []int{port}, Mode: "intercept",
		}),
		AllowAddrs: []string{hostPort(host, port)},
	})

	const payload = "chunked-body-payload"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// A body with no known length: Go sends it chunked and reports
	// ContentLength as -1.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://%s/upload", hostPort(host, port)), iotest.OneByteReader(strings.NewReader(payload)))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := s.client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (logs:\n%s)", resp.StatusCode, s.Logs())
	}

	got := waitForBytesOut(t, s)
	if got != int64(len(payload)) {
		t.Errorf("bytes_out = %d, want %d", got, len(payload))
	}
}

// waitForBytesOut reads the bytes_out column of the single audit row.
func waitForBytesOut(t *testing.T, s *stack) int64 {
	t.Helper()
	waitForRows(t, s, 1)

	db, err := sql.Open("sqlite3", "file:"+filepath.Join(s.dataDir, "audit.db")+"?mode=ro")
	if err != nil {
		t.Fatalf("opening the audit database: %v", err)
	}
	defer func() { _ = db.Close() }()

	var out int64
	if err := db.QueryRow(`SELECT bytes_out FROM audit_records ORDER BY ts LIMIT 1`).Scan(&out); err != nil {
		t.Fatalf("reading bytes_out: %v", err)
	}
	return out
}

// TestNoLeak is AC-10 and the no-leak half of AC-8: an injected credential
// value must appear in none of the four sinks, and neither must an arbitrary
// non-credential header the client sent.
func TestNoLeak(t *testing.T) {
	const (
		credSentinel   = "SENTINEL-CREDENTIAL-VALUE-9f3a"
		headerSentinel = "SENTINEL-ARBITRARY-HEADER-7c2b"
	)

	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny", rule{
			Name: "up", Host: host, Ports: []int{port}, Mode: "intercept",
			Inject: inject(map[string]string{"Authorization": "Bearer ${cred.tok}"}),
		}),
		AllowAddrs: []string{hostPort(host, port)},
		UpstreamCA: up.CertPEM(t),
		EnvCredentials: map[string]any{
			"tok": map[string]any{"var": "TOK", "hosts": []string{host}},
		},
		Env: map[string]string{"TOK": credSentinel},
	})

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://%s/leak-check", hostPort(host, port)), nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("X-Arbitrary-Marker", headerSentinel)

	resp, err := s.client().Do(req)
	if err != nil {
		t.Fatalf("request: %v (logs:\n%s)", err, s.Logs())
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// The injection worked, so the sentinel is genuinely in play — otherwise
	// this test would pass vacuously.
	if got := up.Last(t).Header.Get("Authorization"); got != "Bearer "+credSentinel {
		t.Fatalf("upstream Authorization = %q, want the credential injected", got)
	}
	if got := up.Last(t).Header.Get("X-Arbitrary-Marker"); got != headerSentinel {
		t.Fatalf("upstream marker header = %q, want it forwarded", got)
	}

	rows := waitForRows(t, s, 1)
	joined := strings.Join(rows, "\n")

	// Sink 1: the audit database. Sink 2: the process logs.
	for _, sentinel := range []string{credSentinel, headerSentinel} {
		if strings.Contains(joined, sentinel) {
			t.Errorf("sentinel %q appeared in the audit database:\n%s", sentinel, joined)
		}
		if strings.Contains(s.Logs(), sentinel) {
			t.Errorf("sentinel %q appeared in the process logs", sentinel)
		}
	}

	// Sink 3: every dashboard JSON response, including the live SSE feed.
	for _, route := range []string{"/api/audit", "/api/rules", "/api/credentials"} {
		_, body := authedGet(t, s, route)
		for _, sentinel := range []string{credSentinel, headerSentinel} {
			if strings.Contains(body, sentinel) {
				t.Errorf("sentinel %q appeared in %s:\n%s", sentinel, route, body)
			}
		}
	}

	events := readSSE(t, s, 3*time.Second)
	if events == "" {
		t.Log("note: no SSE events observed in the window; the sweep below is then vacuous for that sink")
	}
	for _, sentinel := range []string{credSentinel, headerSentinel} {
		if strings.Contains(events, sentinel) {
			t.Errorf("sentinel %q appeared in the SSE feed:\n%s", sentinel, events)
		}
	}

	// Sink 4: CLI output.
	out := runCLI(t, s, "credential", "list")
	for _, sentinel := range []string{credSentinel, headerSentinel} {
		if strings.Contains(out, sentinel) {
			t.Errorf("sentinel %q appeared in `credential list` output:\n%s", sentinel, out)
		}
	}
	if !strings.Contains(out, "tok") {
		t.Errorf("`credential list` should still name the credential, so this sweep is not vacuous:\n%s", out)
	}
}

// readSSE subscribes to the live feed, drives one more request so an event is
// produced, and returns whatever arrived within the window.
func readSSE(t *testing.T, s *stack, window time.Duration) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.dashURL("/api/events"), nil)
	if err != nil {
		t.Fatalf("building the SSE request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("subscribing to the SSE feed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Produce an event for the feed to carry.
	go func() {
		time.Sleep(200 * time.Millisecond)
		up, _ := http.NewRequest(http.MethodGet, s.dashURL("/healthz"), nil)
		_, _ = (&http.Client{Timeout: time.Second}).Do(up)
	}()

	buf := make([]byte, 32*1024)
	var collected strings.Builder
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			collected.Write(buf[:n])
		}
		if err != nil {
			break
		}
		if collected.Len() > 16*1024 {
			break
		}
	}
	return collected.String()
}

// runCLI runs an http-broker subcommand against the stack's config.
func runCLI(t *testing.T, s *stack, args ...string) string {
	t.Helper()

	cmd := exec.Command(brokerBinary, args...)
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Dir(s.configDir),
		"XDG_DATA_HOME="+filepath.Dir(s.dataDir),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("`http-broker %s` exited with %v (output kept for the sweep)", strings.Join(args, " "), err)
	}
	return string(out)
}

// TestQueryRedaction is D14: credential-shaped query parameters are stored
// redacted, case-insensitively, including the presigned-URL names.
func TestQueryRedaction(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules:      rulesDoc("deny", rule{Name: "up", Host: host, Ports: []int{port}, Mode: "intercept"}),
		AllowAddrs: []string{hostPort(host, port)},
		UpstreamCA: up.CertPEM(t),
	})

	cases := []struct {
		name  string
		query string
	}{
		{"api_key", "api_key=SENTINELQ1"},
		{"token", "token=SENTINELQ2"},
		{"aws presigned signature", "X-Amz-Signature=SENTINELQ3"},
		{"aws presigned credential", "X-Amz-Credential=SENTINELQ4"},
		{"aws security token", "X-Amz-Security-Token=SENTINELQ5"},
		{"google signature", "X-Goog-Signature=SENTINELQ6"},
		{"uppercase api key", "API_KEY=SENTINELQ7"},
		{"mixed case token", "ToKeN=SENTINELQ8"},
	}

	for _, tc := range cases {
		resp, _ := requestThroughProxy(t, s.client(), http.MethodGet,
			fmt.Sprintf("https://%s/q?%s", hostPort(host, port), tc.query))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", tc.name, resp.StatusCode)
		}
	}

	rows := waitForRows(t, s, len(cases))
	joined := strings.Join(rows, "\n")

	for i := range cases {
		sentinel := fmt.Sprintf("SENTINELQ%d", i+1)
		if strings.Contains(joined, sentinel) {
			t.Errorf("%s: value %q was stored unredacted:\n%s", cases[i].name, sentinel, joined)
		}
	}
	if !strings.Contains(joined, "REDACTED") {
		t.Errorf("no row shows a REDACTED marker, so redaction may not have run at all:\n%s", joined)
	}

	// A non-credential parameter is kept, so redaction is targeted rather than
	// blanket — an audit log with no query at all is much less useful.
	resp, _ := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("https://%s/q?page=7&sort=name", hostPort(host, port)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	rows = waitForRows(t, s, len(cases)+1)
	if !strings.Contains(strings.Join(rows, "\n"), "page=7") {
		t.Error("an ordinary query parameter should be preserved")
	}
}

// TestAuditBlockedOutcomes proves refusals are audited, not just allowed
// requests — an audit log that only records successes is worse than none.
func TestAuditBlockedOutcomes(t *testing.T) {
	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny",
			rule{Name: "ads", Host: "*.doubleclick.net", Mode: "deny"},
		),
	})

	if resp := s.connect("ads.doubleclick.net:443"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if resp := s.connect("unmatched.example.com:443"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}

	rows := waitForRows(t, s, 2)
	joined := strings.Join(rows, "\n")

	if !strings.Contains(joined, "blocked") {
		t.Errorf("refusals should be audited with outcome=blocked:\n%s", joined)
	}
	if !strings.Contains(joined, "ads") {
		t.Errorf("the matched rule name should be recorded:\n%s", joined)
	}
	if !strings.Contains(joined, "fallthrough") {
		t.Errorf("a fallthrough refusal should be attributed as such:\n%s", joined)
	}
}

// TestAuditCredentialFailureTags is AC-8 and AC-9: the two credential failure
// modes are distinguishable in the log.
func TestAuditCredentialFailureTags(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny", rule{
			Name: "up", Host: host, Ports: []int{port}, Mode: "intercept",
			Inject: inject(map[string]string{"Authorization": "Bearer ${cred.absent}"}),
		}),
		AllowAddrs: []string{hostPort(host, port)},
		UpstreamCA: up.CertPEM(t),
	})

	resp, _ := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("https://%s/x", hostPort(host, port)))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}

	rows := waitForRows(t, s, 1)
	if !strings.Contains(strings.Join(rows, "\n"), "credential_unresolved") {
		t.Errorf("the row should be tagged credential_unresolved:\n%s", strings.Join(rows, "\n"))
	}
}

func TestAuditHostScopeTag(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny", rule{
			Name: "up", Host: host, Ports: []int{port}, Mode: "intercept",
			Inject: inject(map[string]string{"Authorization": "Bearer ${cred.elsewhere}"}),
		}),
		AllowAddrs: []string{hostPort(host, port)},
		UpstreamCA: up.CertPEM(t),
		EnvCredentials: map[string]any{
			"elsewhere": map[string]any{"var": "OTHER", "hosts": []string{"api.github.com"}},
		},
		Env: map[string]string{"OTHER": "SENTINEL-SCOPE"},
	})

	resp, _ := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("https://%s/x", hostPort(host, port)))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}

	rows := waitForRows(t, s, 1)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "credential_host_scope_violation") {
		t.Errorf("the row should be tagged credential_host_scope_violation:\n%s", joined)
	}
	if strings.Contains(joined, "SENTINEL-SCOPE") {
		t.Errorf("the credential value appeared in the audit row:\n%s", joined)
	}
}

// TestAuditTunnelRow is AC-4: a tunnel row carries host, port, bytes and
// duration, with NULL method and path.
func TestAuditTunnelRow(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules:      rulesDoc("tunnel", rule{Name: "up", Host: host, Ports: []int{port}, Mode: "tunnel"}),
		AllowAddrs: []string{hostPort(host, port)},
	})

	// Close the tunnel after the handshake: the row is written when the relay
	// finishes, which is the correct moment — the byte counts and duration are
	// not known until then.
	conn, resp := s.connectKeepingConn(hostPort(host, port))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
	}
	_ = conn.Close()

	rows := waitForRows(t, s, 1)
	row := rows[0]

	if !strings.Contains(row, "tunnel") {
		t.Errorf("row %q should record interception=tunnel", row)
	}
	if !strings.Contains(row, host) {
		t.Errorf("row %q should carry the host", row)
	}
	// Columns are joined with "|", so a NULL method and path show as
	// "<nil>" — the point is they are not a fabricated value.
	if strings.Contains(row, "GET") || strings.Contains(row, "/thing") {
		t.Errorf("row %q must not record a method or path for a tunnel", row)
	}
}
