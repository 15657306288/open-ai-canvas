package service

import (
	"context"
	"sync"
	"sync/atomic"
)

type workerRuntime struct {
	mu          sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	started     bool
	draining    atomic.Bool
	activeTasks atomic.Int64
}

func (r *workerRuntime) start() (context.Context, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return r.ctx, false
	}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.started = true
	r.draining.Store(false)
	return r.ctx, true
}

func (r *workerRuntime) goLoop(fn func(context.Context)) bool {
	r.mu.Lock()
	if !r.started || r.draining.Load() {
		r.mu.Unlock()
		return false
	}
	ctx := r.ctx
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		fn(ctx)
	}()
	return true
}

func (r *workerRuntime) goTask(fn func()) bool {
	r.mu.Lock()
	if !r.started || r.draining.Load() {
		r.mu.Unlock()
		return false
	}
	r.wg.Add(1)
	r.activeTasks.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		defer r.activeTasks.Add(-1)
		fn()
	}()
	return true
}

func (r *workerRuntime) beginDrain() {
	r.mu.Lock()
	r.draining.Store(true)
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
}

func (r *workerRuntime) stop(ctx context.Context) error {
	r.beginDrain()
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		r.mu.Lock()
		r.started = false
		r.ctx = nil
		r.cancel = nil
		r.mu.Unlock()
		return nil
	}
}

func (r *workerRuntime) isDraining() bool { return r.draining.Load() }

func (r *workerRuntime) activeTaskCount() int64 { return r.activeTasks.Load() }
