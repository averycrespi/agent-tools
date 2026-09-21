package storage

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"time"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
)

// Snapshot owns one exact SQLite connection and explicitly pinned read
// transaction. Its acquisition deadline does not become its copy deadline.
type Snapshot struct{ connection *sql.Conn }

func PinSnapshot(ctx context.Context, database *sql.DB) (*Snapshot, error) {
	connection, err := database.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = connection.ExecContext(ctx, `BEGIN DEFERRED`); err != nil {
		_ = connection.Close()
		return nil, err
	}
	snapshot := &Snapshot{connection: connection}
	var count int
	if err = connection.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema`).Scan(&count); err != nil {
		return nil, errors.Join(err, snapshot.Close())
	}
	return snapshot, nil
}

func (snapshot *Snapshot) CopyTo(ctx context.Context, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := gatewaypaths.CreateOwnerOnlyFile(destination)
	if err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	uri := &url.URL{Scheme: "file", Path: destination, RawQuery: "mode=rw"}
	err = snapshot.connection.Raw(func(raw any) error {
		connection := raw.(sqliteDriver.Conn).Raw()
		old := connection.SetInterrupt(ctx)
		defer connection.SetInterrupt(old)
		backup, err := connection.BackupInit("main", uri.String())
		if err != nil {
			return err
		}
		for {
			if err = ctx.Err(); err != nil {
				return errors.Join(err, backup.Close())
			}
			done, err := backup.Step(128)
			if err != nil || done {
				return errors.Join(err, backup.Close())
			}
		}
	})
	return errors.Join(err, ctx.Err())
}

func (snapshot *Snapshot) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := snapshot.connection.ExecContext(ctx, `ROLLBACK`)
	return errors.Join(err, snapshot.connection.Close())
}

// WithSnapshotFence refuses occupied control mutation capacity without creating
// an intent or latching healthy authority. The callback only pins read snapshots.
func (store *Store) WithSnapshotFence(ctx context.Context, pin func(*sql.DB) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.acquireMutation(ctx, nil, false); err != nil {
		return err
	}
	defer store.releaseMutation()
	if store.Latched() {
		return ErrStorageLatched
	}
	return pin(store.database)
}
