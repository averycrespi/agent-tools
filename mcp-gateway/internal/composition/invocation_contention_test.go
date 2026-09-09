package composition

import (
	"bytes"
	"context"
	"io"
	"os"

	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/mcpingress"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIngressConcurrencyFourAuditWaitWorkload(t *testing.T) {
	for _, mode := range []string{"warn", "debug", "stalled", "expired"} {
		t.Run(mode, func(t *testing.T) { runIngressDiagnosticWorkload(t, mode) })
	}
}

// Retain lossless storage evidence separately from the intentionally lossy diagnostic sink.
type workloadTerminalEvidence struct {
	delegate    diagnostics.StorageObserver
	ctx         context.Context
	forceExpiry bool
	enabled     atomic.Bool
	mu          sync.Mutex
	attempts    []diagnostics.Facts
	held        bool
	expired     int
	release     chan struct{}
}

func (*workloadTerminalEvidence) DebugEnabled() bool { return true }

func (evidence *workloadTerminalEvidence) Storage(facts diagnostics.Facts) {
	evidence.delegate.Storage(facts)
	if !evidence.enabled.Load() || facts.Writer != diagnostics.TerminalWriter ||
		(facts.Event != diagnostics.StorageAcquire && facts.Event != diagnostics.StorageReject) {
		return
	}
	evidence.mu.Lock()
	evidence.attempts = append(evidence.attempts, facts)
	hold := evidence.forceExpiry && !evidence.held && facts.Event == diagnostics.StorageAcquire
	if hold {
		evidence.held = true
	}
	if facts.Event == diagnostics.StorageReject && facts.Cause == diagnostics.Expired {
		evidence.expired++
		if evidence.expired == 3 {
			close(evidence.release)
		}
	}
	evidence.mu.Unlock()
	if hold {
		// The other three barrier participants must actually expire, not merely sleep.
		select {
		case <-evidence.release:
		case <-evidence.ctx.Done():
		}
	}
}

func runIngressDiagnosticWorkload(t *testing.T, mode string) {
	startedWorkload := time.Now()
	var output bytes.Buffer
	canaries := []string{"parallel.read", "argument-secret-canary", "downstream-secret-canary"}
	var sink io.Writer = &output
	var pipeReader, pipeWriter *os.File
	level := diagnostics.Debug
	if mode == "warn" {
		level = diagnostics.Warn
	}
	if mode == "stalled" {
		var err error
		pipeReader, pipeWriter, err = os.Pipe()
		require.NoError(t, err)
		sink = pipeWriter
	}
	diagnostic := diagnostics.New(sink, level)
	defer func() {
		diagnostic.Finish(nil)
		if pipeReader != nil {
			require.NoError(t, pipeReader.Close())
		}
		<-diagnostic.Done()
		if pipeWriter != nil {
			require.NoError(t, pipeWriter.Close())
		}
		for _, canary := range canaries {
			require.NotContains(t, output.String(), canary)
		}
		if mode == "debug" {
			seen := make(map[uint64]string)
			for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'}) {
				var record struct {
					Event      string `json:"event"`
					Call       uint64 `json:"call_id"`
					Invocation string `json:"invocation_id"`
				}
				require.NoError(t, json.Unmarshal(line, &record))
				if record.Event == "invocation_admission" && record.Invocation != "" {
					require.NotZero(t, record.Call)
					seen[record.Call] = record.Invocation
				}
				if record.Event == "execution_start" {
					require.Equal(t, seen[record.Call], record.Invocation)
				}
			}
			require.NotEmpty(t, seen)
		}
		t.Logf("diagnostics=%s elapsed=%s encoded bytes=%d (fixture observation, not throughput guarantee)", mode, time.Since(startedWorkload), output.Len())
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var calls, active, peak atomic.Int32
	var failureWave, terminalFault atomic.Bool
	var unknown, known atomic.Int32
	var barrierMu sync.Mutex
	arrived := 0
	barrier := make(chan struct{})
	downstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&message) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", contract.MediaTypeJSON)
		result := `{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}`
		switch message.Method {
		case "tools/list":
			result = `{"tools":[{"name":"read","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}]}`
		case "tools/call":
			callIndex := calls.Add(1)
			current := active.Add(1)
			defer active.Add(-1)
			for prior := peak.Load(); current > prior && !peak.CompareAndSwap(prior, current); prior = peak.Load() {
			}
			barrierMu.Lock()
			group := barrier
			arrived++
			if arrived == 4 {
				if failureWave.Load() {
					terminalFault.Store(true)
				}
				close(group)
				arrived = 0
				barrier = make(chan struct{})
			}
			barrierMu.Unlock()
			select {
			case <-group:
			case <-ctx.Done():
				return
			}
			if failureWave.Load() && callIndex == 33 {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = connection.Close() // Accepted request with no response: outcome remains unknown, never retried.
				return
			}
			result = `{"content":[{"type":"text","text":"downstream-secret-canary"}]}`
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, message.ID, result)
	}))
	defer downstreamServer.Close()
	var probe atomic.Bool
	var foreignRejected atomic.Int32
	var store *storage.Store
	options, recoveryRoot, cleanup := newCompositionOptionsWithRecoveryRoot(t, func(point storage.FaultPoint) error {
		if point == storage.FaultAfterCommit && probe.Load() {
			// Conditional probe: foreign writes during actual owner cleanup must reject.
			if err := store.Mutate(ctx, func(*sql.Tx) error { return nil }); errors.Is(err, storage.ErrMutationBusy) {
				foreignRejected.Add(1)
			}
		}
		if point == storage.FaultAfterCommit && terminalFault.CompareAndSwap(true, false) {
			return errors.New("terminal-ack-secret-canary")
		}
		return nil
	})
	defer cleanup()
	store = options.Store
	options.Diagnostics = diagnostic
	// Keep the real catalog timer beyond the fixture lifetime even though its clock is frozen.
	options.Clock = testutil.NewFakeClock(compositionTime.Add(30 * time.Second))
	built, err := New(options)
	require.NoError(t, err)
	terminalEvidence := &workloadTerminalEvidence{delegate: diagnostic, ctx: ctx, forceExpiry: mode == "expired", release: make(chan struct{})}
	store.SetDiagnostics(terminalEvidence)
	defer built.shutdownConstructed()
	server := enableCompositionServer(t, built.servers, createServerWithTransport(t, built.servers, "parallel", contract.StreamableHTTPTransport{
		Kind: contract.TransportStreamableHTTP, URL: downstreamServer.URL + "/mcp", ProtocolMode: contract.ProtocolModern,
		Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone},
	}))
	principal, err := built.authorization.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "parallel fixture", Visibility: contract.VisibilityAll})
	require.NoError(t, err)
	credential, err := built.authorization.IssueCredential(ctx, principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	canaries = append(canaries, credential.Bearer)
	_, err = built.authorization.CreateGrant(ctx, authorization.CreateGrantRequest{PrincipalID: principal.Principal.ID, ServerID: server.ID, Effect: contract.GrantAllow}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
	require.NoError(t, err)
	require.NoError(t, built.Start(ctx))
	require.True(t, built.manager.Wait(ctx), "settle reconciliation before workload")
	require.Eventually(t, func() bool { _, ok := built.activeCatalog.Routes().ResolveCall("parallel.read"); return ok }, time.Second, time.Millisecond)
	bundle, ok := built.AgentIngress()
	require.True(t, ok)
	ingress := mcpingress.New(mcpingress.Options{Authenticator: bundle.Authenticator, ListTools: bundle.ListTools, CallTools: bundle.CallTools})
	defer ingress.Shutdown()
	var authMax atomic.Int64
	request := func(method string) error {
		params := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`
		if method == "tools/call" {
			params += `,"name":"parallel.read","arguments":{"argument-secret-canary":"argument-secret-canary"}`
		}
		r := newAgentRequest(credential.Bearer, fmt.Sprintf(`{"jsonrpc":"2.0","id":"fixture","method":%q,"params":{%s}}`, method, params)).WithContext(ctx)
		started := time.Now()
		authenticated, err := ingress.Authenticate(ctx, r, contract.AuthorityAgent)
		elapsed := time.Since(started).Nanoseconds()
		for prior := authMax.Load(); elapsed > prior && !authMax.CompareAndSwap(prior, elapsed); prior = authMax.Load() {
		}
		if err != nil {
			return fmt.Errorf("authenticate fixture: %w", err)
		}
		response := httptest.NewRecorder()
		ingress.ServeHTTP(response, r.WithContext(authenticated))
		var envelope struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			return err
		}
		if failureWave.Load() && method == "tools/call" && len(envelope.Error) != 0 {
			var rpcError struct {
				Data struct {
					Code string `json:"code"`
				} `json:"data"`
			}
			if json.Unmarshal(envelope.Error, &rpcError) == nil && rpcError.Data.Code == string(contract.OutcomeUnknown) {
				unknown.Add(1)
				return nil
			}
		}
		if response.Code != http.StatusOK || len(envelope.Error) != 0 || len(envelope.Result) == 0 {
			return fmt.Errorf("fixture request failed: %s", response.Body.String())
		}
		if failureWave.Load() && method == "tools/call" {
			known.Add(1)
		}
		return nil
	}
	terminalEvidence.enabled.Store(true)
	probe.Store(true)
	finished := make(chan error, 4)
	for range 4 {
		go func() {
			for range 8 {
				if err := request("tools/list"); err != nil {
					finished <- err
					return
				}
				if err := request("tools/call"); err != nil {
					finished <- err
					return
				}
			}
			finished <- nil
		}()
	}
	for range 4 {
		require.NoError(t, <-finished)
	}
	probe.Store(false)
	terminalEvidence.enabled.Store(false)
	require.EqualValues(t, 32, calls.Load())
	require.EqualValues(t, 4, peak.Load(), "real downstream HTTP calls overlapped")
	var admitted, completed int
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*), count(terminal_class) FROM invocations WHERE decision = 'allow'`).Scan(&admitted, &completed)
	}))
	require.Equal(t, 32, admitted)
	terminalEvidence.mu.Lock()
	attempts := append([]diagnostics.Facts(nil), terminalEvidence.attempts...)
	terminalEvidence.mu.Unlock()
	require.Len(t, attempts, 32, "exactly one synchronous terminal attempt per successful call")
	seen := make(map[uint64]bool)
	acquired, expired := 0, 0
	for _, attempt := range attempts {
		require.NotZero(t, attempt.Call)
		require.False(t, seen[attempt.Call], "terminal attempts must not replay")
		seen[attempt.Call] = true
		if attempt.Event == diagnostics.StorageAcquire {
			require.Equal(t, diagnostics.Success, attempt.Cause)
			acquired++
		} else {
			require.Equal(t, diagnostics.Expired, attempt.Cause, "only bounded acquisition expiry may omit a terminal")
			expired++
		}
	}
	require.Equal(t, acquired, completed, "every acquired terminal mutation must persist")
	require.Equal(t, 32, completed+expired, "every missing terminal needs observed acquisition expiry")
	if mode == "expired" {
		require.GreaterOrEqual(t, expired, 3, "held terminal owner forces the other barrier participants to expire")
	}
	initialCompleted := completed
	commitWindows := int32(32 + completed)
	require.Equal(t, commitWindows, foreignRejected.Load())
	owned, waiting := store.MutationOccupancy()
	require.False(t, owned)
	require.Zero(t, waiting)
	require.False(t, store.Latched())
	t.Logf("32 calls / concurrency 4 / 32 preceding tools/list: %d terminals persisted, %d acquisitions expired; max authentication %s; foreign probes rejected %d/%d owned commit-cleanup windows (conditional samples, not a general rejection rate)", completed, expired, time.Duration(authMax.Load()), foreignRejected.Load(), commitWindows)

	// All four additional calls must reach the downstream barrier before any
	// terminal fault is armed, proving acknowledged audit precedes this wave.
	failureWave.Store(true)
	for range 4 {
		go func() {
			if err := request("tools/list"); err != nil {
				finished <- err
				return
			}
			finished <- request("tools/call")
		}()
	}
	for range 4 {
		require.NoError(t, <-finished)
	}
	require.EqualValues(t, 1, unknown.Load())
	require.EqualValues(t, 3, known.Load(), "known live results survive failed terminal acknowledgment")
	require.EqualValues(t, 36, calls.Load(), "unknown outcomes are never replayed")
	require.True(t, store.Latched())
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*), count(terminal_class) FROM invocations WHERE decision = 'allow'`).Scan(&admitted, &completed)
	}))
	require.Equal(t, 36, admitted)
	require.Equal(t, initialCompleted+1, completed, "only the first terminal row committed before latch; remaining evidence stays unknown")
	owned, waiting = store.MutationOccupancy()
	require.False(t, owned)
	require.Zero(t, waiting)
	_, err = built.authorization.Authenticate(ctx, credential.Bearer)
	require.ErrorIs(t, err, authorization.ErrStorageUnavailable)
	ingress.Shutdown()
	built.shutdownConstructed()
	cleanup() // No active producer or storage owner remains before stopped recovery.
	_, err = storage.VerifyCurrent(ctx, recoveryRoot)
	require.NoError(t, err)
	recoveredOwnership, err := gatewaypaths.Acquire(recoveryRoot)
	require.NoError(t, err)
	defer func() { require.NoError(t, recoveredOwnership.Close()) }()
	recovered, err := storage.Open(ctx, recoveredOwnership)
	require.NoError(t, err)
	defer func() { require.NoError(t, recovered.Close()) }()
	require.False(t, recovered.Latched())
	require.NoError(t, recovered.View(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*), count(terminal_class) FROM invocations WHERE decision = 'allow'`).Scan(&admitted, &completed)
	}))
	require.Equal(t, 36, admitted)
	require.Equal(t, initialCompleted+1, completed)
	require.EqualValues(t, 36, calls.Load(), "stopped recovery never resumes live calls or annotations")
	canaries = append(canaries, "terminal-ack-secret-canary")
	t.Log("additional concurrency-four failure wave: 3 known results, 1 unknown, terminal latch, stopped verification/reopen; no replay")
}

func TestInvocationOverloadBehindCatalogDelaysAuthorityButNotForeignRejection(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var armed atomic.Bool
	options, cleanup := newCompositionOptionsWithFault(t, func(point storage.FaultPoint) error {
		if point == storage.FaultAfterCommit && armed.CompareAndSwap(true, false) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	defer cleanup()
	var output bytes.Buffer
	diagnostic := diagnostics.New(&output, diagnostics.Debug)
	options.Diagnostics = diagnostic
	defer func() {
		require.True(t, diagnostic.Finish(nil))
		<-diagnostic.Done()
		var rejectedCall uint64
		var expiredCall uint64
		for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'}) {
			var record struct {
				Event      string `json:"event"`
				Cause      string `json:"cause"`
				Call       uint64 `json:"call_id"`
				Invocation string `json:"invocation_id"`
			}
			require.NoError(t, json.Unmarshal(line, &record))
			if record.Event == "invocation_admission" {
				require.Empty(t, record.Invocation)
				rejectedCall = record.Call
			}
			if record.Event == "storage_reject" && record.Cause == "expired" {
				expiredCall = record.Call
			}
		}
		require.NotZero(t, rejectedCall)
		require.Equal(t, rejectedCall, expiredCall)
	}()
	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	server := createCompositionServer(t, built.servers, "contender", true, "/bin/true")
	principal, err := built.authorization.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "overload", Visibility: contract.VisibilityAll})
	require.NoError(t, err)
	credential, err := built.authorization.IssueCredential(ctx, principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	lease, err := built.authorization.Authenticate(ctx, credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	fence := catalog.CommitFence{ServerID: server.ID, ExpectedDesiredRevision: server.DesiredRevision,
		ExpectedRegistrationRevision: "0", ExpectedCredentialRevisions: contract.CredentialRevisions{StaticCredential: "0", OAuthClient: "0", OAuthTokens: "0"}, ExpectedCatalogRevision: "0"}
	armed.Store(true)
	catalogDone := make(chan error, 1)
	go func() {
		_, err := built.catalogRepository.SetState(ctx, fence, contract.DurableCatalogUnavailable, 0)
		catalogDone <- err
	}()
	select {
	case <-entered:
	case err := <-catalogDone:
		t.Fatalf("catalog writer rejected before cleanup: %v", err)
	case <-ctx.Done():
		t.Fatal("catalog writer did not reach cleanup")
	}
	callDone := make(chan mcpingress.ToolsCallResponse, 1)
	started := time.Now()
	go func() {
		callDone <- built.callTools.Call(ctx, lease, mcpingress.ToolsCallRequest{Params: contentionCallParams(), WireValid: true})
	}()
	require.Eventually(t, func() bool { _, waiting := options.Store.MutationOccupancy(); return waiting == 1 }, time.Second, time.Millisecond)
	name := "changed"
	foreignStart := time.Now()
	_, err = built.servers.Patch(ctx, server.ID, server.DesiredRevision, servers.Patch{DisplayName: &name})
	require.ErrorIs(t, err, storage.ErrMutationBusy)
	_, err = built.catalogRepository.SetState(ctx, fence, contract.DurableCatalogUnavailable, 0)
	require.ErrorIs(t, err, storage.ErrMutationBusy)
	foreignElapsed := time.Since(foreignStart)
	require.Less(t, foreignElapsed, contract.InvocationMutationWaitDeadline)
	authCtx, authCancel := context.WithTimeout(ctx, 40*time.Millisecond)
	authStart := time.Now()
	_, err = built.authorization.Authenticate(authCtx, credential.Bearer)
	authElapsed := time.Since(authStart)
	authCancel()
	require.ErrorIs(t, err, context.DeadlineExceeded, "queued invocation holds authority, delaying authentication")
	policyCtx, policyCancel := context.WithTimeout(ctx, 40*time.Millisecond)
	_, err = built.authorization.PatchPrincipal(policyCtx, principal.Principal.ID, authorization.PatchPrincipalRequest{ExpectedRevision: credential.Principal.Revision, DisplayName: &name})
	policyCancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	response := <-callDone
	require.Equal(t, contract.AuditUnavailable, response.ErrorCode)
	require.Empty(t, response.InvocationID)
	require.GreaterOrEqual(t, time.Since(started), contract.InvocationMutationWaitDeadline)
	require.Less(t, time.Since(started), time.Second)
	// Expiry released authority even though the foreign writer has not settled.
	freshLease, err := built.authorization.Authenticate(ctx, credential.Bearer)
	require.NoError(t, err)
	freshLease.Release()
	_, err = built.authorization.PatchPrincipal(ctx, principal.Principal.ID, authorization.PatchPrincipalRequest{ExpectedRevision: credential.Principal.Revision, DisplayName: &name})
	require.ErrorIs(t, err, storage.ErrMutationBusy, "authority mutations still do not wait for storage")
	unblock()
	require.NoError(t, <-catalogDone)
	count, err := built.invocationRepository.Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
	require.False(t, options.Store.Latched())
	t.Logf("bounded overload: authentication deadline observed after %s; two foreign catalog/control mutations rejected in %s; invocation expired at the 250ms bound without a row", authElapsed, foreignElapsed)
}

func contentionCallParams() strictjson.Value {
	return strictjson.Value{Type: strictjson.ValueObject, Object: []strictjson.Member{
		{Name: "name", Value: strictjson.Value{Type: strictjson.ValueString, String: "mcp_gateway.get_identity"}},
		{Name: "arguments", Value: strictjson.Value{Type: strictjson.ValueObject}},
	}}
}

func TestCompositionDrainWakesInvocationStorageWaitBeforeForeignCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	principal, err := built.authorization.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "drain fixture", Visibility: contract.VisibilityAll})
	require.NoError(t, err)
	credential, err := built.authorization.IssueCredential(ctx, principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	lease, err := built.authorization.Authenticate(ctx, credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	foreign := make(chan error, 1)
	go func() {
		foreign <- options.Store.Mutate(ctx, func(*sql.Tx) error {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil
		})
	}()
	<-entered
	response := make(chan mcpingress.ToolsCallResponse, 1)
	go func() {
		response <- built.callTools.Call(ctx, lease, mcpingress.ToolsCallRequest{Params: contentionCallParams(), WireValid: true})
	}()
	require.Eventually(t, func() bool { _, waiting := options.Store.MutationOccupancy(); return waiting == 1 }, time.Second, time.Millisecond)
	drained := built.Drain(ctx)
	select {
	case <-lease.Done():
	case <-ctx.Done():
		t.Fatal("pending lease not cancelled")
	}
	select {
	case call := <-response:
		require.Equal(t, contract.AuditUnavailable, call.ErrorCode)
		require.Empty(t, call.InvocationID)
	case <-ctx.Done():
		t.Fatal("queued invocation did not wake")
	}
	owned, waiting := options.Store.MutationOccupancy()
	require.True(t, owned, "foreign active owner remains through settlement")
	require.Zero(t, waiting)
	unblock()
	require.NoError(t, <-foreign)
	<-drained
	require.NoError(t, options.Store.Mutate(ctx, func(*sql.Tx) error { return nil }), "producer cleanup remains available")
	count, err := built.invocationRepository.Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, count)
	assert.False(t, options.Store.Latched())
}
