// Package workerpool provides a bounded-concurrency task pool with graceful
// shutdown, used by the GC worker and parallel chunk assembly.
package workerpool

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed is returned by Submit after Shutdown has begun.
var ErrClosed = errors.New("workerpool: pool closed")

// Task is a unit of work. The context is the pool's run context; tasks must
// honor its cancellation.
type Task func(ctx context.Context)

// Pool runs tasks on a fixed set of goroutines. Zero value is not usable;
// construct with New.
type Pool struct {
	tasks  chan Task
	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
}

// New starts a pool with the given number of workers and task queue depth.
func New(workers, queueDepth int) *Pool {
	if workers < 1 {
		workers = 1
	}
	if queueDepth < 0 {
		queueDepth = 0
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{tasks: make(chan Task, queueDepth), ctx: ctx, cancel: cancel}
	p.wg.Add(workers)
	for i := 0; i < workers; i++ {
		go p.worker()
	}
	return p
}

func (p *Pool) worker() {
	defer p.wg.Done()
	for task := range p.tasks {
		task(p.ctx)
	}
}

// Submit enqueues a task, blocking while the queue is full. It returns
// ErrClosed once shutdown has started, or ctx.Err() if the caller's context
// ends while waiting for queue space.
func (p *Pool) Submit(ctx context.Context, t Task) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrClosed
	}
	p.mu.Unlock()

	select {
	case p.tasks <- t:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ctx.Done():
		return ErrClosed
	}
}

// Shutdown stops accepting tasks and waits for queued and running tasks to
// finish, or for ctx to expire — in which case running tasks are canceled
// via the pool context and Shutdown returns ctx.Err().
func (p *Pool) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.tasks)
	}
	p.mu.Unlock()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		p.cancel()
		return nil
	case <-ctx.Done():
		p.cancel() // signal tasks to stop
		<-done
		return ctx.Err()
	}
}
