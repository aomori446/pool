package pool

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrNoMoreJobs is a sentinel error used to signal the pool to shut down.
	ErrNoMoreJobs = errors.New("no more jobs")

	// ErrPoolClosed is an error indicating that a job was interrupted because the pool was shut down.
	ErrPoolClosed = errors.New("pool was closed during job execution")
)

// Job represents a unit of work to be executed.
type Job[R any] struct {
	Execute func(ctx context.Context) (R, error)
	Context context.Context
	Timeout time.Duration
	Retries int
	Backoff time.Duration
}

// NewNoMoreJobsSignal creates a special job that signals the pool to stop accepting new jobs.
func NewNoMoreJobsSignal[R any]() Job[R] {
	return Job[R]{
		Execute: func(ctx context.Context) (R, error) {
			var zero R
			return zero, ErrNoMoreJobs
		},
	}
}

// Result holds the value and a potential error from a completed job.
type Result[V any] struct {
	Error error
	Value V
}

// Stats holds metrics about the pool's performance.
type Stats struct {
	Pending   int64
	Completed int64
	Failed    int64
}

// Pool manages a pool of workers for concurrent job execution.
type Pool[R any] struct {
	jobs    chan Job[R]
	results chan Result[R]

	closeOnce sync.Once
	wg        sync.WaitGroup
	done      chan struct{}

	mu           sync.Mutex
	workers      int
	workerCancel chan struct{}

	pendingJobs   atomic.Int64
	completedJobs atomic.Int64
	failedJobs    atomic.Int64
}

// NewPool creates and starts a new worker pool with an initial number of workers.
func NewPool[R any](jobBufferSize int, initialWorkers int) *Pool[R] {
	if jobBufferSize < 0 {
		jobBufferSize = 0
	}

	if initialWorkers < 1 {
		initialWorkers = 1
	}

	pool := &Pool[R]{
		jobs:         make(chan Job[R], jobBufferSize),
		results:      make(chan Result[R], initialWorkers),
		done:         make(chan struct{}),
		workerCancel: make(chan struct{}),
	}

	pool.Resize(initialWorkers)

	go func() {
		pool.wg.Wait()
		close(pool.results)
	}()

	return pool
}

func (p *Pool[R]) worker() {
	defer p.wg.Done()
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	for {
		select {
		case <-p.done:
			return
		case <-p.workerCancel:
			return
		case job, ok := <-p.jobs:
			if !ok {
				return
			}

			func(job Job[R]) {
				p.pendingJobs.Add(-1)

				ctx := job.Context
				if ctx == nil {
					ctx = context.Background()
				}

				if job.Timeout > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, job.Timeout)
					defer cancel()
				}

				var val R
				var err error

			RetryLoop:
				for i := 0; i <= job.Retries; i++ {
					val, err = job.Execute(ctx)
					if err == nil {
						break
					}
					if i < job.Retries && job.Backoff > 0 {
						backoffCeiling := job.Backoff * time.Duration(1<<i)
						sleepTime := time.Duration(r.Intn(int(backoffCeiling)))
						select {
						case <-time.After(sleepTime):
						case <-ctx.Done():
							err = ctx.Err()
							break RetryLoop
						case <-p.done:
							err = ErrPoolClosed
							break RetryLoop
						}
					}
				}

				if errors.Is(err, ErrNoMoreJobs) {
					p.Close()
					return
				}

				if err == nil {
					p.completedJobs.Add(1)
				} else {
					p.failedJobs.Add(1)
				}

				result := Result[R]{Error: err, Value: val}

				select {
				case p.results <- result:
				case <-p.done:
				}
			}(job)
		}
	}
}

// Resize dynamically changes the number of running workers.
func (p *Pool[R]) Resize(n int) {
	if n < 0 {
		n = 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	for p.workers < n {
		p.wg.Add(1)
		go p.worker()
		p.workers++
	}

	for p.workers > n {
		p.workerCancel <- struct{}{}
		p.workers--
	}
}

// Workers returns the current desired number of workers.
func (p *Pool[R]) Workers() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.workers
}

// Stats returns a snapshot of the pool's current metrics.
func (p *Pool[R]) Stats() Stats {
	return Stats{
		Pending:   p.pendingJobs.Load(),
		Completed: p.completedJobs.Load(),
		Failed:    p.failedJobs.Load(),
	}
}

// Close gracefully shuts down the worker pool.
func (p *Pool[R]) Close() {
	p.closeOnce.Do(func() {
		close(p.done)
	})
}

// Results returns the channel from which to consume job results.
func (p *Pool[R]) Results() <-chan Result[R] {
	return p.results
}

// Submit adds a job to the pool for execution.
func (p *Pool[R]) Submit(job Job[R]) {
	if job.Execute == nil {
		return
	}
	select {
	case <-p.done:
		return
	case p.jobs <- job:
		p.pendingJobs.Add(1)
	}
}
