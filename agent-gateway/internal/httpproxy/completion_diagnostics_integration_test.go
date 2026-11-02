//go:build integration

package httpproxy

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
)

type proxyCompletionLosses struct {
	counts [len(diagnostics.TrafficLossCounts{})]atomic.Uint64
}

func (*proxyCompletionLosses) Traffic(diagnostics.Facts) {}

func (losses *proxyCompletionLosses) TrafficLoss(reason diagnostics.TrafficLossReason, _ bool, count uint64) {
	if int(reason) < len(losses.counts) {
		losses.counts[reason].Add(count)
	}
}

// Keep protocol assertions and deadlines unchanged while distinguishing missing
// optional evidence from a retained but incorrect completion on hosted failures.
func diagnoseProxyCompletionOnFailure(t *testing.T, f *proxyFixture) {
	t.Helper()
	losses := new(proxyCompletionLosses)
	f.traffic.SetTrafficDiagnostics(losses)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		history, err := f.traffic.HTTPHistory(ctx, 0, 10)
		t.Logf("completion history: read_failed=%t records=%d", err != nil, len(history.Records))
		for _, record := range history.Records {
			if c := record.Completion; c != nil {
				t.Logf("retained completion: source=%s outcome=%s status=%d gateway_status=%d bytes=%d", c.ResponseSource, c.Outcome, c.Status, c.GatewayStatus, c.BytesReceived)
			}
		}
		status := f.traffic.Status(ctx)
		engine := f.engine.Status()
		t.Logf("completion delivery: state=%s accounting=%t accepted=%d acknowledged=%d discarded=%d queued=%d terminal_queued=%d active_streams=%d work=%d", status.State, status.AccountingAvailable, status.Delivery.Accepted, status.Delivery.Acknowledged, status.Delivery.Discarded, status.Delivery.QueueRecords, status.Delivery.CompletionRecords, engine.ActiveStreams, engine.Work.InUse)
		t.Logf("completion losses: expired=%d full=%d writer=%d invalid=%d unavailable=%d disabled=%d reservation=%d identity=%d oversized=%d", losses.counts[diagnostics.TrafficQueueExpired].Load(), losses.counts[diagnostics.TrafficQueueFull].Load(), losses.counts[diagnostics.TrafficWriterLoss].Load(), losses.counts[diagnostics.TrafficInvalid].Load(), losses.counts[diagnostics.TrafficUnavailable].Load(), losses.counts[diagnostics.TrafficDisabled].Load(), losses.counts[diagnostics.TrafficReservation].Load(), losses.counts[diagnostics.TrafficIdentity].Load(), losses.counts[diagnostics.TrafficOversized].Load())
	})
}
