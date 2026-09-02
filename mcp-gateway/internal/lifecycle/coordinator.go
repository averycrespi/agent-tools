package lifecycle

import (
	"context"
	"sync"
	"sync/atomic"
)

type Coordinator struct {
	ctx       context.Context
	cancel    context.CancelFunc
	force     func()
	draining  atomic.Bool
	forceOnce sync.Once
}

func New(parent context.Context, force func()) *Coordinator {
	ctx, cancel := context.WithCancel(parent)
	if force == nil {
		force = func() {}
	}
	return &Coordinator{ctx: ctx, cancel: cancel, force: force}
}

func (coordinator *Coordinator) Context() context.Context { return coordinator.ctx }
func (coordinator *Coordinator) Draining() bool           { return coordinator.draining.Load() }

func (coordinator *Coordinator) Signal() {
	if coordinator.draining.CompareAndSwap(false, true) {
		coordinator.cancel()
		return
	}
	coordinator.forceOnce.Do(coordinator.force)
}
