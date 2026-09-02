//go:build integration

package invocation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepositoryRetainsNewest65536ByMonotonicSequence(t *testing.T) {
	repository, store, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(1))
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		return insertInvocationCapacityFixtures(context.Background(), transaction, invocationLimit())
	}))
	cursor, err := encodeInvocationCursor(contract.InvocationCursorBinding{UpperSequence: invocationLimit(), NextSequence: 1})
	require.NoError(t, err)
	prepared, err := repository.Prepare(testEvaluatedAdmission())
	require.NoError(t, err)
	require.NoError(t, repository.Insert(context.Background(), prepared))
	count, err := repository.Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, invocationLimit(), count)
	_, found, err := repository.Read(context.Background(), capacityInvocationID(101))
	require.NoError(t, err)
	assert.False(t, found)
	oldest, found, err := repository.Read(context.Background(), capacityInvocationID(102))
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(2), oldest.Sequence)
	newest, found, err := repository.Read(context.Background(), prepared.InvocationID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, invocationLimit()+1, newest.Sequence)
	_, err = repository.List(context.Background(), contract.InvocationListQuery{Limit: 1, Cursor: &cursor})
	assert.ErrorIs(t, err, ErrStaleCursor)
	_, err = repository.Get(context.Background(), capacityInvocationID(101))
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestRepositoryRollsBackEvictionWhenInsertFails(t *testing.T) {
	const limit = 3
	repository, store, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(limit+1))
	repository.limit = limit
	prepared := make([]PreparedAdmission, limit+1)
	for index := range prepared {
		var err error
		prepared[index], err = repository.Prepare(testEvaluatedAdmission())
		require.NoError(t, err)
	}
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		return insertInvocationFixtures(context.Background(), transaction, prepared[:limit])
	}))
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, err := transaction.ExecContext(context.Background(), `CREATE TRIGGER reject_test_invocation BEFORE INSERT ON invocations
			WHEN NEW.id = '`+prepared[limit].InvocationID+`' BEGIN SELECT RAISE(ABORT, 'test rejection'); END`)
		return err
	}))
	assert.ErrorIs(t, repository.Insert(context.Background(), prepared[limit]), ErrStorageUnavailable)
	count, err := repository.Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(limit), count)
	_, found, err := repository.Read(context.Background(), prepared[0].InvocationID)
	require.NoError(t, err)
	assert.True(t, found, "the eviction must roll back with the failed insertion")
}

func capacityInvocationID(value int64) string { return fmt.Sprintf("%026d", value) }

func insertInvocationCapacityFixtures(ctx context.Context, transaction *sql.Tx, count int64) error {
	_, err := transaction.ExecContext(ctx, `WITH RECURSIVE fixtures(sequence) AS (
		SELECT 1 UNION ALL SELECT sequence + 1 FROM fixtures WHERE sequence < ?
	) INSERT INTO invocations (
		id, principal_id, credential_id, credential_fingerprint, credential_revision,
		admitted_at, admission_class, requested_name, redacted_arguments,
		server_id, tool_id, upstream_name, descriptor_revision, descriptor_fingerprint,
		decision, authorization_revision, evaluated_at, grant_id
	) SELECT printf('%026d', sequence + 100), printf('%026d', 1), printf('%026d', 2),
		'0123456789abcdef', 1, '2026-08-26T19:00:00.000000000Z', 'invalid_params',
		NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL
	FROM fixtures`, count)
	return err
}

func insertInvocationFixtures(ctx context.Context, transaction *sql.Tx, prepared []PreparedAdmission) error {
	const columns = `INSERT INTO invocations (
		id, principal_id, credential_id, credential_fingerprint, credential_revision,
		admitted_at, admission_class, requested_name, redacted_arguments,
		server_id, tool_id, upstream_name, descriptor_revision, descriptor_fingerprint,
		decision, authorization_revision, evaluated_at, grant_id
	) VALUES `
	const row = `(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	const batchSize = 50
	for start := 0; start < len(prepared); start += batchSize {
		end := min(start+batchSize, len(prepared))
		arguments := make([]any, 0, (end-start)*18)
		for _, admission := range prepared[start:end] {
			values, err := admissionSQLValues(admission)
			if err != nil {
				return err
			}
			arguments = append(arguments, values...)
		}
		if _, err := transaction.ExecContext(ctx, columns+strings.TrimSuffix(strings.Repeat(row+",", end-start), ","), arguments...); err != nil {
			return err
		}
	}
	return nil
}

func uniqueInvocationEntropy(count int) io.Reader {
	value := make([]byte, count*10)
	for index := 0; index < count; index++ {
		binary.BigEndian.PutUint64(value[index*10+2:(index+1)*10], uint64(index+1))
	}
	return bytes.NewReader(value)
}
