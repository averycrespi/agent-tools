package invocation

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

func BenchmarkTrafficWriteRead(b *testing.B) {
	owner, err := gatewaypaths.Acquire(filepath.Join(b.TempDir(), "installation"))
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	config := DefaultTrafficConfig()
	config.BudgetBytes = 8 << 20
	config.RetainedRecords = 128
	s, err := CreateTraffic(b.Context(), owner, invocationTestInstallationID, invocationID(90), config)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(b.Context(), 5*time.Second)
		observation := TrafficObservation{prepared: trafficPrepared(i + 1)}
		observation.bytes = trafficCharge(observation.prepared)
		_, err = s.writeTraffic(ctx, []*trafficRequest{{observation: observation}})
		cancel()
		if err != nil {
			b.Fatal(err)
		}
		if _, err = s.History(b.Context(), 0, 128); err != nil {
			b.Fatal(err)
		}
	}
}
