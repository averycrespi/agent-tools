package invocation

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

func (s *TrafficStore) Status(ctx context.Context) contract.TrafficStatus {
	if s.optional != nil {
		return s.optionalStatus(ctx)
	}
	s.mu.Lock()
	status := contract.TrafficStatus{Ready: !s.closed && !s.faulted && !s.draining, Faulted: s.faulted, Pressure: s.queued >= s.config.QueueRecords || s.queuedBytes >= s.config.QueueBytes, BudgetBytes: s.config.BudgetBytes, QuotaRefusals: s.quotaRefusals, RollingHistory: true, UnknownCompletionPossible: true}
	status.State = "ready"
	if s.faulted {
		status.State = "faulted"
	} else if s.closed || s.draining {
		status.State = "disabled"
	}
	status.Health = s.healthLocked()
	status.LastAcknowledged = s.lastAcknowledged
	status.Incident = s.incidentLocked()
	status.Delivery = s.deliveryLocked()
	status.PressureReason = s.pressureReason
	if status.Pressure {
		status.PressureReason = "queue_capacity"
	}
	if status.PressureReason == "" {
		status.PressureReason = "none"
	}
	s.mu.Unlock()
	status.DatabaseMeasurement = measureBytes(s.path, false)
	status.WALMeasurement = measureBytes(s.path+"-wal", true)
	status.FreeSpaceMeasurement = measureFreeSpace(filepath.Dir(s.path))
	if status.DatabaseMeasurement.Bytes != nil {
		status.DatabaseBytes = *status.DatabaseMeasurement.Bytes
	}
	if status.WALMeasurement.Bytes != nil {
		status.WALBytes = *status.WALMeasurement.Bytes
	}
	if status.DatabaseMeasurement.State == "unavailable" || status.WALMeasurement.State == "unavailable" {
		status.Pressure = true
		status.PressureReason = "measurement_unavailable"
	} else if status.DatabaseBytes+status.WALBytes+trafficReservation(s.config) > status.BudgetBytes {
		status.PressureReason = "budget_reservation"
		status.Pressure = true
	}
	if status.FreeSpaceMeasurement.Bytes != nil && *status.FreeSpaceMeasurement.Bytes < trafficReservation(s.config) {
		status.Pressure = true
		status.PressureReason = "low_space"
	}
	err := s.view(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT generation,pruning FROM traffic_meta WHERE singleton=1`).Scan(&status.Generation, &status.PrunedRecords)
	})
	status.AccountingAvailable = err == nil
	if err != nil {
		status.Ready = false
		status.Pressure = true
		if status.State == "ready" {
			status.State = "unavailable"
		}
	}
	// A reachable read can discover a failure after the initial snapshot.
	s.mu.Lock()
	status.Health, status.Incident = s.healthLocked(), s.incidentLocked()
	status.Faulted = s.faulted
	if s.faulted {
		status.Ready = false
		status.State = "faulted"
	}
	s.mu.Unlock()
	return status
}

func (s *TrafficStore) deliveryLocked() contract.DeliveryCounters {
	refusals := s.quotaRefusals
	if refusals < 0 {
		refusals = 0
	}
	return contract.DeliveryCounters{Accepted: s.accepted, Acknowledged: s.acknowledged, Discarded: uint64(refusals), QueueRecords: s.queued, QueueBytes: s.queuedBytes, CompletionRecords: s.completionQueued, QueueRecordLimit: s.config.QueueRecords, QueueByteLimit: s.config.QueueBytes}
}

func measureFreeSpace(path string) contract.ByteMeasurement {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil || stat.Bsize <= 0 || stat.Bavail > contract.RecordedActivityMaxCount/uint64(stat.Bsize) {
		return contract.ByteMeasurement{State: "unavailable"}
	}
	available := stat.Bavail * uint64(stat.Bsize)
	if available > contract.RecordedActivityMaxCount {
		return contract.ByteMeasurement{State: "unavailable"}
	}
	bytes := int64(available)
	return contract.ByteMeasurement{State: "available", Bytes: &bytes}
}

func measureBytes(path string, absent bool) contract.ByteMeasurement {
	info, err := os.Stat(path)
	if absent && errors.Is(err, os.ErrNotExist) {
		return contract.ByteMeasurement{State: "absent"}
	}
	if err != nil || !info.Mode().IsRegular() {
		return contract.ByteMeasurement{State: "unavailable"}
	}
	bytes := info.Size()
	return contract.ByteMeasurement{State: "available", Bytes: &bytes}
}
