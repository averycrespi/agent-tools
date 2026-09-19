package invocation

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// BackupPair pins two coherent read transactions while both actual writers and
// cross-store admissions are fenced. Copying occurs after the short fence is
// released, on the exact pinned connections. Live admitted executions continue.
func (s *TrafficStore) BackupPair(ctx context.Context, control *storage.Store, controlPath, trafficPath string) (result error) {
	started := time.Now()
	if !s.admissionGate.TryLock() {
		return ErrTrafficCapacity
	}
	if !s.writerGate.TryLock() {
		s.admissionGate.Unlock()
		return ErrTrafficCapacity
	}
	if !s.readGate.TryRLock() {
		s.writerGate.Unlock()
		s.admissionGate.Unlock()
		return ErrTrafficCapacity
	}
	defer s.readGate.RUnlock()
	snapshotCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var controlSnapshot, trafficSnapshot *storage.Snapshot
	defer func() {
		if controlSnapshot != nil {
			result = errors.Join(result, controlSnapshot.Close())
		}
		if trafficSnapshot != nil {
			result = errors.Join(result, trafficSnapshot.Close())
		}
	}()
	pauseCtx, pauseCancel := context.WithTimeout(ctx, time.Second)
	defer pauseCancel()
	err := control.WithSnapshotFence(pauseCtx, func(database *sql.DB) error {
		if !s.Healthy() {
			return ErrTrafficFault
		}
		if err := s.inject("snapshot_fence"); err != nil {
			return err
		}
		var err error
		controlSnapshot, err = storage.PinSnapshot(pauseCtx, database)
		if err != nil {
			return err
		}
		trafficSnapshot, err = storage.PinSnapshot(pauseCtx, s.readerDB)
		return err
	})
	s.writerGate.Unlock()
	s.admissionGate.Unlock()
	// Wall-clock overrun is a failed backup even if every SQL call returned nil.
	if err != nil || pauseCtx.Err() != nil || time.Since(started) > time.Second {
		return errors.Join(err, ErrTrafficDeadline)
	}
	if err = s.inject("snapshots_pinned"); err != nil {
		return err
	}
	if err = controlSnapshot.CopyTo(snapshotCtx, controlPath); err != nil {
		return err
	}
	if err = trafficSnapshot.CopyTo(snapshotCtx, trafficPath); err != nil {
		return err
	}
	return snapshotCtx.Err()
}
