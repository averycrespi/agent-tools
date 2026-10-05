package invocation

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOptionalTrafficOwnsWriterThroughClose(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	// Register release before fixture cleanup so a failed assertion cannot leave
	// its real writer parked behind the test barrier.
	target, _ := trafficFixture(t, nil, func(point string) error {
		if point == "commit" {
			close(entered)
			<-release
		}
		return nil
	})
	t.Cleanup(unblock)
	facade := NewOptionalTraffic(target.config)
	facade.StartOpening(func() (*TrafficStore, error) { return target, nil })
	<-facade.optional.done
	require.True(t, facade.Healthy())
	facade.ObserveMCP(trafficPrepared(1))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not reach commit")
	}
	facade.BeginDrain()
	closed := make(chan error, 1)
	go func() { closed <- facade.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("close released unsettled writer: %v", err)
	default:
	}
	require.False(t, facade.Healthy())
	unblock()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("close did not settle")
	}
}

func TestOptionalTrafficDisabledIsNotObservedZero(t *testing.T) {
	facade := NewOptionalTraffic(DefaultTrafficConfig())
	facade.StartOpening(nil)
	defer func() { require.NoError(t, facade.Close()) }()
	require.Equal(t, "disabled", facade.Status(t.Context()).State)
	facade.ObserveMCP(trafficPrepared(1))
	require.EqualValues(t, 1, facade.Status(t.Context()).QuotaRefusals)
	summary := facade.RecordedActivity()
	require.Equal(t, "unavailable", summary.Coverage)
	for _, bucket := range summary.Buckets {
		require.Nil(t, bucket.Counts)
	}
}
