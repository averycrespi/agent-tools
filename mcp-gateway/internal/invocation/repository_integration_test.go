//go:build integration

package invocation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepositoryRetainsNewest65536ByMonotonicSequence(t *testing.T) {
	repository, store, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(int(invocationLimit())+1))
	prepared := make([]PreparedAdmission, int(invocationLimit())+1)
	for index := range prepared {
		var err error
		prepared[index], err = repository.Prepare(testEvaluatedAdmission())
		require.NoError(t, err)
	}
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		if err := insertInvocationFixtures(context.Background(), transaction, prepared[:invocationLimit()]); err != nil {
			return err
		}
		return repository.InsertTx(context.Background(), transaction, prepared[invocationLimit()])
	}))
	count, err := repository.Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, invocationLimit(), count)
	_, found, err := repository.Read(context.Background(), prepared[0].InvocationID)
	require.NoError(t, err)
	assert.False(t, found)
	oldest, found, err := repository.Read(context.Background(), prepared[1].InvocationID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(2), oldest.Sequence)
	newest, found, err := repository.Read(context.Background(), prepared[len(prepared)-1].InvocationID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, invocationLimit()+1, newest.Sequence)
	require.NoError(t, repository.ValidateStartup(context.Background()))
}

func TestRepositoryRollsBackEvictionWhenInsertFails(t *testing.T) {
	repository, store, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(int(invocationLimit())+1))
	prepared := make([]PreparedAdmission, int(invocationLimit())+1)
	for index := range prepared {
		var err error
		prepared[index], err = repository.Prepare(testEvaluatedAdmission())
		require.NoError(t, err)
	}
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		return insertInvocationFixtures(context.Background(), transaction, prepared[:invocationLimit()])
	}))
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, err := transaction.ExecContext(context.Background(), `CREATE TRIGGER reject_test_invocation BEFORE INSERT ON invocations
			WHEN NEW.id = '`+prepared[invocationLimit()].InvocationID+`' BEGIN SELECT RAISE(ABORT, 'test rejection'); END`)
		return err
	}))
	assert.ErrorIs(t, repository.Insert(context.Background(), prepared[invocationLimit()]), ErrStorageUnavailable)
	count, err := repository.Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, invocationLimit(), count)
	_, found, err := repository.Read(context.Background(), prepared[0].InvocationID)
	require.NoError(t, err)
	assert.True(t, found, "the eviction must roll back with the failed insertion")
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
