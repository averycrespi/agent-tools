package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testInstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

var testNow = time.Date(2026, 8, 22, 18, 0, 0, 0, time.UTC)

func TestAdministratorAuditAttributionAndAtomicity(t *testing.T) {
	ctx := t.Context()
	store, _ := newStore(t)
	service := NewService(store, testutil.NewFakeClock(testNow), testutil.NewFakeEntropy(append(bytes.Repeat([]byte{0x42}, 42), bytes.Repeat([]byte{0x43}, 42)...)))
	sink := new(memorySink)
	initial, err := service.Initialize(ctx, sink)
	require.NoError(t, err)
	repository, err := audit.NewRepository(store)
	require.NoError(t, err)
	page, err := repository.List(ctx, audit.Query{Limit: 100, Filters: contract.AuditFilters{Category: "admin_credential"}})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, contract.AuditOffline, page.Items[0].Actor.Type)
	assert.Equal(t, "initialize", page.Items[0].Action)
	assert.Equal(t, "succeeded", page.Items[0].Outcome)
	assert.Equal(t, "pending", page.Items[1].Outcome)
	assert.Equal(t, page.Items[0].CorrelationID, page.Items[1].CorrelationID)
	operator := contract.AuditCredential{ID: initial.ID, Fingerprint: initial.Fingerprint}
	ctx = audit.WithOperator(ctx, operator, testInstallationID)
	created, err := service.Create(ctx, nil)
	require.NoError(t, err)
	page, err = repository.List(ctx, audit.Query{Limit: 100, Filters: contract.AuditFilters{Action: "create"}})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, &operator, page.Items[0].Actor.Credential)
	assert.Equal(t, testInstallationID, page.Items[0].CorrelationID)
	assert.Equal(t, created.ID, page.Items[0].Target.ID)
	for _, summary := range page.Items {
		item, err := repository.Read(ctx, summary.ID, page.History.Generation)
		require.NoError(t, err)
		contents, err := json.Marshal(item)
		require.NoError(t, err)
		assert.NotContains(t, string(contents), sink.value)
		assert.NotContains(t, string(contents), created.Bearer)
	}
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER reject_audit_outcome BEFORE INSERT ON control_audit_events WHEN json_extract(NEW.event, '$.phase') = 'outcome' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
		return err
	}))
	require.Error(t, service.Revoke(ctx, created.ID))
	_, err = service.Authenticate(ctx, created.Bearer)
	require.NoError(t, err, "revocation must roll back when its outcome cannot commit")
	page, err = repository.List(ctx, audit.Query{Limit: 100, Filters: contract.AuditFilters{Action: "revoke"}})
	require.NoError(t, err)
	assert.Empty(t, page.Items, "the transaction's attempted fact must roll back too")
}

func TestOfflineAuditFailurePreventsSecretPublication(t *testing.T) {
	ctx := t.Context()
	store, _ := newStore(t)
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER reject_audit BEFORE INSERT ON control_audit_events BEGIN SELECT RAISE(ABORT, 'injected'); END`)
		return err
	}))
	service := NewService(store, testutil.NewFakeClock(testNow), testutil.NewFakeEntropy(bytes.Repeat([]byte{0x42}, 42)))
	sink := new(memorySink)
	_, err := service.Initialize(ctx, sink)
	require.Error(t, err)
	assert.Empty(t, sink.value)
	items, err := service.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestInitializeAndResetPublishBeforeActivatingAuthority(t *testing.T) {
	ctx := context.Background()
	store, databasePath := newStore(t)
	entropy := testutil.NewFakeEntropy(append(
		bytes.Repeat([]byte{0x42}, 42),
		bytes.Repeat([]byte{0x43}, 42)...,
	))
	service := NewService(store, testutil.NewFakeClock(testNow), entropy)

	initialSink := new(memorySink)
	initial, err := service.Initialize(ctx, initialSink)
	require.NoError(t, err)
	require.NotEmpty(t, initialSink.value)
	assert.NotContains(t, initialSink.value, "\n")
	assert.Equal(t, "active", string(initial.Status))
	assert.True(t, initial.NonExpiring)
	assert.Equal(t, "1", initial.Revision)

	authenticated, err := service.Authenticate(ctx, initialSink.value)
	require.NoError(t, err)
	assert.Equal(t, initial.ID, authenticated.ID)

	resetSink := new(memorySink)
	reset, err := service.Reset(ctx, resetSink)
	require.NoError(t, err)
	require.NotEqual(t, initialSink.value, resetSink.value)
	assert.Equal(t, "2", reset.Revision)

	_, err = service.Authenticate(ctx, initialSink.value)
	assert.ErrorIs(t, err, ErrAuthenticationRequired)
	_, err = service.Authenticate(ctx, resetSink.value)
	require.NoError(t, err)

	databaseBytes, err := os.ReadFile(databasePath)
	require.NoError(t, err)
	assert.NotContains(t, string(databaseBytes), initialSink.value)
	assert.NotContains(t, string(databaseBytes), resetSink.value)
}

func TestAdminCredentialSensitiveSinks(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	service := NewService(store, testutil.NewFakeClock(testNow), testutil.NewFakeEntropy(append(
		bytes.Repeat([]byte{0x61}, 42),
		bytes.Repeat([]byte{0x62}, 42)...,
	)))
	knownSink := new(memorySink)
	known, err := service.Initialize(ctx, knownSink)
	require.NoError(t, err)

	failedSink := newFileSecretSink("ignored", func(string) (secretFile, error) {
		return nil, errors.New("sink unavailable")
	})
	_, err = service.Reset(ctx, failedSink)
	assert.ErrorIs(t, err, ErrSecretPublication)
	authenticated, err := service.Authenticate(ctx, knownSink.value)
	require.NoError(t, err)
	assert.Equal(t, known.ID, authenticated.ID, "offline authority must remain current until its one-time sink publishes")
}

func TestSinkFailureNeverActivatesUndisclosedAuthority(t *testing.T) {
	failures := []struct {
		name string
		file *scriptedSecretFile
		open error
	}{
		{name: "open", open: errors.New("open failed")},
		{name: "write", file: &scriptedSecretFile{writeErr: errors.New("write failed")}},
		{name: "short write", file: &scriptedSecretFile{writeLimit: 8}},
		{name: "sync", file: &scriptedSecretFile{syncErr: errors.New("sync failed")}},
		{name: "close", file: &scriptedSecretFile{closeErr: errors.New("close failed")}},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			ctx := context.Background()
			store, _ := newStore(t)
			service := NewService(store, testutil.NewFakeClock(testNow), testutil.NewFakeEntropy(append(
				bytes.Repeat([]byte{0x24}, 42),
				bytes.Repeat([]byte{0x25}, 42)...,
			)))
			knownSink := new(memorySink)
			known, err := service.Initialize(ctx, knownSink)
			require.NoError(t, err)

			sink := newFileSecretSink("ignored", func(string) (secretFile, error) {
				return failure.file, failure.open
			})
			_, err = service.Reset(ctx, sink)
			assert.ErrorIs(t, err, ErrSecretPublication)

			authenticated, err := service.Authenticate(ctx, knownSink.value)
			require.NoError(t, err)
			assert.Equal(t, known.ID, authenticated.ID)
			if failure.file != nil && failure.file.buffer.Len() > 0 {
				candidate := failure.file.buffer.String()
				candidate = candidate[:len(candidate)-1]
				_, err = service.Authenticate(ctx, candidate)
				assert.ErrorIs(t, err, ErrAuthenticationRequired)
			}
		})
	}
}

func TestOwnerOnlyFileSinkIsExclusiveAndExact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authority")
	sink := NewFileSecretSink(path)
	require.NoError(t, sink.Publish("candidate"))

	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "candidate\n", string(contents))
	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	assert.Error(t, sink.Publish("replacement"))
	contents, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "candidate\n", string(contents))
}

func TestInitializeRejectsSecondAuthority(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	service := NewService(store, testutil.NewFakeClock(testNow), testutil.NewFakeEntropy(bytes.Repeat([]byte{0x33}, 84)))
	require.NoError(t, mustInitialize(service, ctx))

	_, err := service.Initialize(ctx, new(memorySink))
	assert.ErrorIs(t, err, ErrAlreadyInitialized)
}

func newStore(t *testing.T) (*storage.Store, string) {
	t.Helper()
	ownership, err := gatewaypaths.Acquire(filepath.Join(t.TempDir(), "gateway"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ownership.Close()) })
	store, err := storage.Initialize(context.Background(), ownership, testInstallationID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store, ownership.Layout().Database
}

func mustInitialize(service *Service, ctx context.Context) error {
	_, err := service.Initialize(ctx, new(memorySink))
	return err
}

type memorySink struct {
	value string
}

func (sink *memorySink) Publish(value string) error {
	sink.value = value
	return nil
}

type scriptedSecretFile struct {
	buffer     bytes.Buffer
	writeErr   error
	writeLimit int
	syncErr    error
	closeErr   error
}

func (file *scriptedSecretFile) Write(value []byte) (int, error) {
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	if file.writeLimit > 0 && len(value) > file.writeLimit {
		value = value[:file.writeLimit]
	}
	return file.buffer.Write(value)
}

func (file *scriptedSecretFile) Sync() error  { return file.syncErr }
func (file *scriptedSecretFile) Close() error { return file.closeErr }

var _ io.WriteCloser = (*scriptedSecretFile)(nil)
