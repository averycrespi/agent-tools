package diagnostics

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func validCallStart(id uint64) Facts {
	return Facts{Event: ExecutionStart, Call: id, InvocationID: "01ARZ3NDEKTSV4RRFFQ69G5FA0"}
}

func records(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var result []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		require.LessOrEqual(t, len(line)+1, RecordBytes)
		var record map[string]any
		require.NoError(t, json.Unmarshal(line, &record))
		result = append(result, record)
	}
	return result
}
func TestDiagnosticLevelsAndClosedSchema(t *testing.T) {
	for _, test := range []struct {
		value  string
		level  Level
		events int
	}{{"warn", Warn, 1}, {"info", Info, 2}, {"debug", Debug, 3}} {
		t.Run(test.value, func(t *testing.T) {
			parsed, ok := ParseLevel(test.value)
			require.True(t, ok)
			require.Equal(t, test.level, parsed)
			var sink bytes.Buffer
			adapter := New(&sink, parsed)
			adapter.Observe(Facts{Event: Startup})
			adapter.Observe(validCallStart(1))
			adapter.Observe(Facts{Event: LifecycleFailure, Cause: Unavailable})
			require.True(t, adapter.Finish(nil))
			got := records(t, sink.Bytes())
			require.Len(t, got, test.events)
			for _, record := range got {
				require.EqualValues(t, 1, record["schema_version"])
				require.NotEmpty(t, record["process_id"])
			}
		})
	}
	for _, value := range []string{"", "error", "DEBUG", "warn\n{}", "debug "} {
		_, ok := ParseLevel(value)
		require.False(t, ok)
	}
	for _, facts := range []Facts{{Event: 255}, {Event: Startup, Call: 1}, {Event: ExecutionResult, Stage: IntentArm}, {Event: StorageAcquire, InvocationID: strings.Repeat("0", 26)}, {Event: ExecutionResult, InvocationID: "token\n{\"event\":\"forged\"}"}, {Event: ExecutionResult, InvocationID: strings.Repeat("SECRET", 1<<18)}, {Event: AuthorityAcquire, Waiting: 33}, {Event: StorageLatch, Cause: 255}, {Event: DurabilityFailure, Stage: 255}, {Event: StorageWait, Writer: 255}, {Event: ExecutionStart, Duration: -1}} {
		require.False(t, validFacts(facts))
	}
}

type blockingSink struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	buffer  bytes.Buffer
	calls   atomic.Int32
}

func (sink *blockingSink) Write(data []byte) (int, error) {
	sink.calls.Add(1)
	sink.once.Do(func() { close(sink.entered) })
	<-sink.release
	return sink.buffer.Write(data)
}
func TestDiagnosticDropNewestBoundedFlushAndJoin(t *testing.T) {
	sink := &blockingSink{entered: make(chan struct{}), release: make(chan struct{})}
	adapter := New(sink, Debug)
	defer func() { close(sink.release); <-adapter.Done() }()
	adapter.Observe(validCallStart(1))
	<-sink.entered
	for i := uint64(2); i <= QueueRecords+1; i++ {
		adapter.Observe(validCallStart(i))
	}
	require.Len(t, adapter.queue, QueueRecords-1)
	require.EqualValues(t, 1, adapter.dropped.Load())
	started := time.Now()
	require.False(t, adapter.Finish(func(writer io.Writer) { _, _ = io.WriteString(writer, "terminal problem\n") }))
	require.GreaterOrEqual(t, time.Since(started), FlushDeadline)
	require.Less(t, time.Since(started), FlushDeadline+500*time.Millisecond)
	require.EqualValues(t, 1, sink.calls.Load())
	adapter.Observe(validCallStart(999))
	require.Len(t, adapter.queue, QueueRecords-1)
}
func TestDiagnosticDropNewestRetentionAndLoss(t *testing.T) {
	sink := &blockingSink{entered: make(chan struct{}), release: make(chan struct{})}
	adapter := New(sink, Debug)
	adapter.Observe(validCallStart(1))
	<-sink.entered
	for i := uint64(2); i <= QueueRecords+1; i++ {
		adapter.Observe(validCallStart(i))
	}
	adapter.Observe(Facts{Event: 255, InvocationID: "secret"})
	close(sink.release)
	require.True(t, adapter.Finish(nil))
	<-adapter.Done()
	got := records(t, sink.buffer.Bytes())
	calls := 0
	loss := 0
	for _, record := range got {
		if record["event"] == "diagnostic_loss" {
			loss++
			require.EqualValues(t, 1, record["dropped"])
			require.EqualValues(t, 1, record["invalid"])
			continue
		}
		calls++
		require.EqualValues(t, calls, record["call_id"])
	}
	require.Equal(t, QueueRecords, calls)
	require.Equal(t, 1, loss)
	require.NotContains(t, sink.buffer.String(), "secret")
}
func TestDiagnosticRealBlockedPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	// A fixture owns both ends and explicitly releases the arbitrary blocked Write.
	adapter := New(writer, Debug)
	defer func() { require.NoError(t, reader.Close()); <-adapter.Done(); require.NoError(t, writer.Close()) }()
	for range 100000 {
		adapter.Observe(validCallStart(1))
	}
	started := time.Now()
	require.False(t, adapter.Finish(nil))
	require.Less(t, time.Since(started), FlushDeadline+500*time.Millisecond)
	select {
	case <-adapter.Done():
		t.Fatal("pipe worker unexpectedly settled without fixture release")
	default:
	}
}

type damagedSink struct {
	calls int
	n     int
	err   error
}

func (sink *damagedSink) Write(data []byte) (int, error) {
	sink.calls++
	if sink.n < 0 {
		return len(data), sink.err
	}
	return sink.n, sink.err
}
func TestDiagnosticShortAndFailingWritesAreNotReplayed(t *testing.T) {
	for _, test := range []struct {
		name string
		n    int
		err  error
	}{{"short", 1, nil}, {"zero", 0, nil}, {"partial error", 1, errors.New("SECRET downstream error")}, {"failed", 0, errors.New("SECRET token")}, {"full error", -1, errors.New("SECRET")}} {
		t.Run(test.name, func(t *testing.T) {
			sink := &damagedSink{n: test.n, err: test.err}
			adapter := New(sink, Debug)
			for range 3 {
				adapter.Observe(Facts{Event: Startup})
			}
			adapter.Finish(func(writer io.Writer) { _, _ = io.WriteString(writer, "terminal\n") })
			<-adapter.Done()
			require.Equal(t, 1, sink.calls)
		})
	}
}
func TestDiagnosticTerminalSharesOneWriter(t *testing.T) {
	var sink bytes.Buffer
	adapter := New(&sink, Debug)
	adapter.Observe(Facts{Event: Startup})
	require.True(t, adapter.Finish(func(writer io.Writer) { _, _ = io.WriteString(writer, "{\"code\":\"serve_stopped\"}\n") }))
	got := records(t, sink.Bytes())
	require.Len(t, got, 2)
	require.Equal(t, "startup", got[0]["event"])
	require.Equal(t, "serve_stopped", got[1]["code"])
}
func TestDiagnosticExecutableEventManifest(t *testing.T) {
	inventory := contract.DiagnosticEvents()
	require.Len(t, inventory, len(eventNames)-1)
	for index, event := range inventory {
		require.Equal(t, event.Name, eventNames[index+1])
		var output bytes.Buffer
		adapter := &Adapter{sink: &output, abort: make(chan struct{}), process: "fixture"}
		facts := validEventExample(Event(index + 1))
		if facts.Event != Loss {
			require.True(t, validFacts(facts), event.Name)
		}
		require.True(t, adapter.encode(facts, 0, 0))
		got := records(t, output.Bytes())
		require.Len(t, got, 1)
		require.Equal(t, strings.ToUpper(event.Level), got[0]["level"])
		for _, key := range event.RequiredFields {
			require.Contains(t, got[0], key, event.Name)
		}
		allowed := append([]string{"schema_version", "time", "level", "event", "process_id"}, event.RequiredFields...)
		allowed = append(allowed, event.OptionalFields...)
		for key := range got[0] {
			require.Contains(t, allowed, key, event.Name)
		}
	}
}

func validEventExample(event Event) Facts {
	f := Facts{Event: event}
	switch {
	case event == Startup || event == Readiness || event == Drain || event == Loss || event == ReconciliationDisplaced:
	case event == Shutdown:
		f.Cause = Success
	case event == LifecycleFailure || event == ReconciliationSettlementFailure:
		f.Cause = Unavailable
	case event <= TerminalAnnotation:
		f.Call = 1
		f.InvocationID = "01ARZ3NDEKTSV4RRFFQ69G5FA0"
		if event != ExecutionStart {
			f.Cause = Success
		}
	case event <= StorageReject:
		f.Mutation = 1
		f.Owned = 1
		f.Limit = 32
		if event >= StorageWait {
			f.Limit = 31
		}
		if event == AuthorityReject || event == StorageReject {
			f.Cause = Capacity
		} else if event != AuthorityWait && event != StorageWait {
			f.Cause = Success
		}
	default:
		f.Mutation = 1
		f.Cause = Latched
		f.Stage = IntentArm
	}
	return f
}

func TestDiagnosticPerEventRequiredFieldsAndCauses(t *testing.T) {
	for _, definition := range contract.DiagnosticEvents() {
		var event Event
		for index, name := range eventNames {
			if name == definition.Name {
				event = Event(index)
			}
		}
		if event == Loss {
			continue
		}
		base := validEventExample(event)
		for cause := None; cause <= UnknownOutcome; cause++ {
			candidate := base
			candidate.Cause = cause
			allowed := false
			for _, name := range definition.Causes {
				if causeNames[cause] == name {
					allowed = true
				}
			}
			// A successful admission requires acknowledgment; unavailable/stopped forbid it.
			if event == InvocationAdmission && (cause == Unavailable || cause == Stopped) {
				candidate.InvocationID = ""
			}
			require.Equal(t, allowed, validFacts(candidate), "%s cause=%s", definition.Name, causeNames[cause])
		}
		for _, field := range definition.RequiredFields {
			candidate := base
			switch field {
			case "call_id":
				candidate.Call = 0
			case "mutation_id":
				candidate.Mutation = 0
			case "invocation_id":
				candidate.InvocationID = ""
			case "cause":
				candidate.Cause = None
			case "stage":
				candidate.Stage = NoStage
			case "limit":
				candidate.Limit = 0
			default:
				continue // Zero is a valid encoded occupancy/writer value, not omission.
			}
			require.False(t, validFacts(candidate), "%s missing %s", definition.Name, field)
		}
	}
	for _, candidate := range []Facts{
		{Event: ExecutionStart, Call: 1},
		{Event: InvocationAdmission, Call: 1, Cause: Success},
		{Event: AuthorityAcquire, Mutation: 1, Limit: 32, Cause: UnknownOutcome},
		{Event: StorageWait, Mutation: 1, Limit: 31, Writer: TerminalWriter},
		{Event: DurabilityFailure, Mutation: 1, Cause: Latched},
		{Event: ExecutionResult, Call: 1, InvocationID: "01ARZ3NDEKTSV4RRFFQ69G5FA0", Cause: Latched},
	} {
		require.False(t, validFacts(candidate))
	}
	var output bytes.Buffer
	adapter := New(&output, Debug)
	adapter.Observe(Facts{Event: ExecutionStart, Call: 1})
	adapter.Authority(Facts{Event: InvocationAdmission, Call: 1, Cause: Rejected})
	require.True(t, adapter.Finish(nil))
	got := records(t, output.Bytes())
	require.Len(t, got, 1)
	require.Equal(t, "diagnostic_loss", got[0]["event"])
	require.EqualValues(t, 2, got[0]["invalid"])
}

func TestDiagnosticSaturatingCounters(t *testing.T) {
	var counter atomic.Uint64
	counter.Store(^uint64(0) - 1)
	require.Equal(t, ^uint64(0), NextID(&counter))
	require.Zero(t, NextID(&counter))
	require.Equal(t, ^uint64(0), counter.Load())
}
