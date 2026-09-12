package lifecycle

import (
	"context"
	"testing"
)

func TestFirstSignalDrainsAndSecondForcesWithoutWaiting(t *testing.T) {
	forced := make(chan struct{}, 1)
	coordinator := New(context.Background(), func() { forced <- struct{}{} })
	if coordinator.Draining() {
		t.Fatal("new coordinator is draining")
	}

	coordinator.Signal()
	if !coordinator.Draining() {
		t.Fatal("first signal did not enter drain")
	}
	select {
	case <-coordinator.Context().Done():
	default:
		t.Fatal("first signal did not cancel service context")
	}
	select {
	case <-forced:
		t.Fatal("first signal forced exit")
	default:
	}

	coordinator.Signal()
	select {
	case <-forced:
	default:
		t.Fatal("second signal did not force exit")
	}
}
