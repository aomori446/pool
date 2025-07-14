package pool

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"
)

var (
	ErrPoolClosed = errors.New("pool was closed during job execution")
)

// controller 負責管理 worker 的生命週期和數量。
type controller struct {
	mu     sync.Mutex
	count  int
	cancel chan struct{}
	wg     sync.WaitGroup
}

// lifecycle 負責管理整個 Pool 的關閉信號。
type lifecycle struct {
	done      chan struct{}
	drainOnce sync.Once
	closeOnce sync.Once
}

// Pool 是一個管理 worker goroutine 池的結構。
// 其成員被組織到幾個內嵌結構中以提高可讀性。
type Pool[R any] struct {
	jobs    chan Job[R]
	results chan Result[R]

	controller controller
	lifecycle  lifecycle
	stats      Stats
}

// NewPool 創建一個新的工作池。
func NewPool[R any](jobBufferSize int, initialWorkers int) *Pool[R] {
	if jobBufferSize < 0 {
		jobBufferSize = 0
	}
	if initialWorkers < 1 {
		initialWorkers = 1
	}

	pool := &Pool[R]{
		jobs:    make(chan Job[R], jobBufferSize),
		results: make(chan Result[R], jobBufferSize),
		controller: controller{
			cancel: make(chan struct{}, 1024),
		},
		lifecycle: lifecycle{
			done: make(chan struct{}),
		},
	}

	pool.Resize(initialWorkers)

	go func() {
		// 等待所有 worker 結束後，安全地關閉 results 通道
		pool.controller.wg.Wait()
		close(pool.results)
	}()

	return pool
}

func (p *Pool[R]) worker() {
	defer p.controller.wg.Done()
	for {
		select {
		case <-p.lifecycle.done: // 全局關閉信號
			return
		case <-p.controller.cancel: // 單個 worker 縮減信號
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
	p.stats.pending.Add(-1)

	baseCtx := job.Context
	if baseCtx == nil {
		baseCtx = context.Background()
	}

	jobCtx, cancel := context.WithCancel(baseCtx)
	defer cancel()

	go func() {
		select {
		case <-p.lifecycle.done:
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
		p.stats.completed.Add(1)
	} else {
		p.stats.failed.Add(1)
	}

	select {
	case p.results <- Result[R]{Error: err, Value: val}:
	case <-p.lifecycle.done:
	}
}

func (p *Pool[R]) isClosing() bool {
	select {
	case <-p.lifecycle.done:
		return true
	default:
		return false
	}
}

// Drain 等待所有已提交的工作被接收，然後關閉工作通道。
func (p *Pool[R]) Drain() {
	p.lifecycle.drainOnce.Do(func() {
		close(p.jobs)
	})
}

// ShutdownNow 立即停止所有 worker 並關閉工作池。
func (p *Pool[R]) ShutdownNow() {
	p.lifecycle.closeOnce.Do(func() {
		close(p.lifecycle.done)
	})
}

// Resize 動態調整 worker 的數量。
func (p *Pool[R]) Resize(n int) {
	if n < 0 {
		n = 0
	}

	if p.isClosing() {
		return
	}

	p.controller.mu.Lock()
	defer p.controller.mu.Unlock()

	for p.controller.count < n {
		p.controller.wg.Add(1)
		go p.worker()
		p.controller.count++
	}

	for p.controller.count > n {
		p.controller.cancel <- struct{}{}
		p.controller.count--
	}
}

// Workers 返回當前 worker 的數量。
func (p *Pool[R]) Workers() int {
	p.controller.mu.Lock()
	defer p.controller.mu.Unlock()
	return p.controller.count
}

// Stats 返回工作池的當前統計數據。
func (p *Pool[R]) Stats() Stats {
	return p.stats
}

// Results 返回一個唯讀的結果通道。
func (p *Pool[R]) Results() <-chan Result[R] {
	return p.results
}

// Submit 將一個工作提交到工作池。
func (p *Pool[R]) Submit(job Job[R]) error {
	if job.Execute == nil {
		return errors.New("job.Execute is nil")
	}
	select {
	case <-p.lifecycle.done:
		return ErrPoolClosed
	case p.jobs <- job:
		p.stats.pending.Add(1)
		return nil
	}
}

// TrySubmit 嘗試將一個工作提交到工作池。
func (p *Pool[R]) TrySubmit(job Job[R]) (bool, error) {
	if job.Execute == nil {
		return false, errors.New("job.Execute is nil")
	}
	select {
	case <-p.lifecycle.done:
		return false, ErrPoolClosed
	case p.jobs <- job:
		p.stats.pending.Add(1)
		return true, nil
	default:
		return false, nil
	}
}
