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
	ErrPoolClosed = errors.New("pool was closed during job execution")
)

type Pool[R any] struct {
	jobs      chan Job[R]
	results   chan Result[R]
	drainOnce sync.Once
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

func NewPool[R any](jobBufferSize int, initialWorkers int) *Pool[R] {
	if jobBufferSize < 0 {
		jobBufferSize = 0
	}
	if initialWorkers < 1 {
		initialWorkers = 1
	}

	pool := &Pool[R]{
		jobs:         make(chan Job[R], jobBufferSize),
		results:      make(chan Result[R], jobBufferSize),
		done:         make(chan struct{}),
		workerCancel: make(chan struct{}, 1024),
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
			p.processJob(job)
		}
	}
}

func (p *Pool[R]) processJob(job Job[R]) {
	p.pendingJobs.Add(-1)

	baseCtx := job.Context
	if baseCtx == nil {
		baseCtx = context.Background()
	}

	jobCtx, cancel := context.WithCancel(baseCtx)
	defer cancel()

	go func() {
		select {
		case <-p.done:
			cancel()
		case <-jobCtx.Done():
			return
		}
	}()

	executeCtx := jobCtx
	if job.Timeout > 0 {
		var timeoutCancel context.CancelFunc
		executeCtx, timeoutCancel = context.WithTimeout(jobCtx, job.Timeout)
		defer timeoutCancel()
	}

	var val R
	var err error

RetryLoop:
	for i := 0; i <= job.Retries; i++ {
		val, err = job.Execute(executeCtx)
		if err == nil {
			break
		}
		if i < job.Retries && job.Backoff > 0 {
			backoffCeiling := job.Backoff * time.Duration(1<<i)
			sleepTime := time.Duration(rand.Intn(int(backoffCeiling)))
			select {
			case <-time.After(sleepTime):
			case <-executeCtx.Done():
				err = executeCtx.Err()
				break RetryLoop
			}
		}
	}

	if errors.Is(err, context.Canceled) && p.isClosing() {
		err = ErrPoolClosed
	}

	if err == nil {
		p.completedJobs.Add(1)
	} else {
		p.failedJobs.Add(1)
	}

	select {
	case p.results <- Result[R]{Error: err, Value: val}:
	case <-p.done:
	}
}

func (p *Pool[R]) isClosing() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *Pool[R]) Drain() {
	p.drainOnce.Do(func() {
		close(p.jobs)
	})
}

func (p *Pool[R]) ShutdownNow() {
	p.closeOnce.Do(func() {
		close(p.done)
	})
}

func (p *Pool[R]) Resize(n int) {
	if n < 0 {
		n = 0
	}

	select {
	case <-p.done:
		return
	default:
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

func (p *Pool[R]) Workers() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.workers
}

func (p *Pool[R]) Stats() Stats {
	return Stats{
		Pending:   p.pendingJobs.Load(),
		Completed: p.completedJobs.Load(),
		Failed:    p.failedJobs.Load(),
	}
}

func (p *Pool[R]) Results() <-chan Result[R] {
	return p.results
}

func (p *Pool[R]) Submit(job Job[R]) error {
	if job.Execute == nil {
		return errors.New("job.Execute is nil")
	}
	select {
	case <-p.done:
		return ErrPoolClosed
	case p.jobs <- job:
		p.pendingJobs.Add(1)
		return nil
	}
}

func (p *Pool[R]) TrySubmit(job Job[R]) (bool, error) {
	if job.Execute == nil {
		return false, errors.New("job.Execute is nil")
	}
	select {
	case <-p.done:
		return false, ErrPoolClosed
	case p.jobs <- job:
		p.pendingJobs.Add(1)
		return true, nil
	default:
		return false, nil
	}
}
