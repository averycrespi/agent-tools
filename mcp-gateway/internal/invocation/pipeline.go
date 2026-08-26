package invocation

import (
	"context"
	"sync"
)

type PipelineFence struct {
	mu       sync.Mutex
	draining bool
	active   int
	idle     chan struct{}
}

func NewPipelineFence() *PipelineFence {
	idle := make(chan struct{})
	close(idle)
	return &PipelineFence{idle: idle}
}

func (fence *PipelineFence) TryEnter() (func(), bool) {
	if fence == nil {
		return nil, false
	}
	fence.mu.Lock()
	if fence.draining {
		fence.mu.Unlock()
		return nil, false
	}
	if fence.active == 0 {
		fence.idle = make(chan struct{})
	}
	fence.active++
	fence.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			fence.mu.Lock()
			fence.active--
			if fence.active == 0 {
				close(fence.idle)
			}
			fence.mu.Unlock()
		})
	}, true
}

func (fence *PipelineFence) BeginDrain() {
	if fence == nil {
		return
	}
	fence.mu.Lock()
	fence.draining = true
	fence.mu.Unlock()
}

func (fence *PipelineFence) Drain(ctx context.Context) error {
	if fence == nil {
		return nil
	}
	fence.BeginDrain()
	fence.mu.Lock()
	idle := fence.idle
	fence.mu.Unlock()
	select {
	case <-idle:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
