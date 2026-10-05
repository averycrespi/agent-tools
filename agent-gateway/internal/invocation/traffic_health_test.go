package invocation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrafficMeasurementsUnavailableNotZero(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	require.Equal(t, "unavailable", measureBytes(missing, false).State)
	require.Equal(t, "absent", measureBytes(missing, true).State)
	require.Nil(t, measureBytes(missing, true).Bytes)
	require.Equal(t, "unavailable", measureBytes(root, false).State)
	require.Equal(t, "unavailable", measureFreeSpace(missing).State)
	require.NoError(t, os.WriteFile(missing, nil, 0600))
	measurement := measureBytes(missing, false)
	require.Equal(t, "available", measurement.State)
	require.NotNil(t, measurement.Bytes)
	require.Zero(t, *measurement.Bytes)
}

func TestTrafficHealthDeliveryNotRows(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	observation := s.ObserveMCP(trafficPrepared(1))
	require.NotNil(t, observation)
	waitTraffic(t, s)
	first := s.Status(t.Context())
	require.EqualValues(t, 1, first.Delivery.Accepted)
	require.EqualValues(t, 1, first.Delivery.Acknowledged)
	require.Zero(t, first.Delivery.QueueRecords)
	require.True(t, first.AccountingAvailable)
	// Duplicate delivery is acknowledged without becoming another retained row.
	s.observeInitial(observation)
	waitTraffic(t, s)
	second := s.Status(t.Context())
	require.EqualValues(t, 2, second.Delivery.Accepted)
	require.EqualValues(t, 2, second.Delivery.Acknowledged)
	require.Zero(t, second.Delivery.Discarded)
}

func TestUnavailableHistoryStillCountsDiscarded(t *testing.T) {
	facade := NewOptionalTraffic(DefaultTrafficConfig())
	facade.StartOpening(nil)
	defer func() { require.NoError(t, facade.Close()) }()
	for range 3 {
		facade.ObserveMCP(trafficPrepared(1))
	}
	s := facade.Status(t.Context())
	require.EqualValues(t, 3, s.Delivery.Discarded)
	require.Zero(t, s.Delivery.Acknowledged)
	require.False(t, s.AccountingAvailable)
	require.Equal(t, "unavailable", s.DatabaseMeasurement.State)
	require.Nil(t, s.DatabaseMeasurement.Bytes)
}
