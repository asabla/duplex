package app

import (
	"context"
	"errors"
	"sync"
	"time"
)

const maxSharedWork = 128

type sharedWork[T any] struct {
	mu     sync.Mutex
	jobs   map[string]*sharedJob[T]
	wg     sync.WaitGroup
	closed bool
}

type sharedJob[T any] struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	value   T
	err     error
}

// Each waiter owns its own deadline. The shared job stops after its last waiter,
// application shutdown, or the hard operation deadline. Canceled jobs stay in
// the bounded map until their process has exited; retries cannot overlap them.
func (g *sharedWork[T]) do(ctx, owner context.Context, key string, run func(context.Context) (T, error)) (T, bool, error) {
	var zero T

	for {
		if err := ctx.Err(); err != nil {
			return zero, false, err
		}

		g.mu.Lock()
		if g.closed {
			g.mu.Unlock()
			return zero, false, context.Canceled
		}

		if job := g.jobs[key]; job != nil {
			if job.waiters == 0 {
				g.mu.Unlock()

				select {
				case <-ctx.Done():
					return zero, false, ctx.Err()
				case <-job.done:
					continue
				}
			}

			job.waiters++
			g.mu.Unlock()

			return g.wait(ctx, job, true)
		}

		if len(g.jobs) >= maxSharedWork {
			g.mu.Unlock()
			return zero, false, errors.New("shared source work capacity reached")
		}

		job := g.startLocked(ctx, owner, key, run)
		g.mu.Unlock()

		return g.wait(ctx, job, false)
	}
}

func (g *sharedWork[T]) startLocked(ctx, owner context.Context, key string, run func(context.Context) (T, error)) *sharedJob[T] {
	if g.jobs == nil {
		g.jobs = map[string]*sharedJob[T]{}
	}

	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)

	stop := func() bool { return false }
	if owner != nil {
		stop = context.AfterFunc(owner, cancel)
	}

	job := &sharedJob[T]{done: make(chan struct{}), cancel: cancel, waiters: 1}
	g.jobs[key] = job

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer cancel()
		defer stop()

		value, err := run(work)
		if work.Err() != nil {
			err = work.Err()
		}

		g.mu.Lock()
		job.value, job.err = value, err

		delete(g.jobs, key)
		close(job.done)
		g.mu.Unlock()
	}()

	return job
}

func (g *sharedWork[T]) wait(ctx context.Context, job *sharedJob[T], shared bool) (T, bool, error) {
	defer func() {
		g.mu.Lock()

		job.waiters--
		if job.waiters == 0 {
			job.cancel()
		}

		g.mu.Unlock()
	}()

	select {
	case <-ctx.Done():
		var zero T
		return zero, shared, ctx.Err()
	case <-job.done:
		return job.value, shared, job.err
	}
}

func (g *sharedWork[T]) close() {
	g.mu.Lock()

	g.closed = true
	for _, job := range g.jobs {
		job.cancel()
	}

	g.mu.Unlock()
	g.wg.Wait()
}
