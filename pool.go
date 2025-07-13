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
	// ErrPoolClosed 表示任務因 pool 已關閉而被中斷。
	ErrPoolClosed = errors.New("pool was closed during job execution")
)

// Job 代表一個要執行的工作單元。
type Job[R any] struct {
	Execute func(ctx context.Context) (R, error)
	Context context.Context
	Timeout time.Duration
	Retries int
	Backoff time.Duration
}

// Result 保存已完成任務的回傳值和潛在錯誤。
type Result[V any] struct {
	Error error
	Value V
}

// Stats 保存 pool 的效能指標。
type Stats struct {
	Pending   int64
	Completed int64
	Failed    int64
}

// Pool 管理一個用於併發執行任務的 worker pool。
type Pool[R any] struct {
	jobs    chan Job[R]
	results chan Result[R]

	drainOnce sync.Once // 用於確保 Drain 只執行一次
	closeOnce sync.Once // 用於確保 ShutdownNow 只執行一次
	wg        sync.WaitGroup
	done      chan struct{} // 用於廣播 pool 關閉信號

	mu           sync.Mutex
	workers      int
	workerCancel chan struct{} // 用於縮減 worker 數量

	pendingJobs   atomic.Int64
	completedJobs atomic.Int64
	failedJobs    atomic.Int64
}

// NewPool 創建並啟動一個新的 worker pool。
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
		workerCancel: make(chan struct{}),
	}

	pool.Resize(initialWorkers)

	go func() {
		pool.wg.Wait()
		close(pool.results)
	}()

	return pool
}

// worker 的職責是從佇列中接收任務，並交由 processJob 處理。
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

// processJob 負責處理單一任務的完整生命週期。
func (p *Pool[R]) processJob(job Job[R]) {
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
			sleepTime := time.Duration(rand.Intn(int(backoffCeiling)))
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

// Drain 優雅地關閉 worker pool。
// 此方法會安全地關閉任務佇列，讓 worker 處理完所有已提交的任務後再停止。
// 這是推薦的、標準的關閉方法，且可以安全地重複呼叫。
func (p *Pool[R]) Drain() {
	p.drainOnce.Do(func() {
		close(p.jobs)
	})
}

// ShutdownNow 立即強制關閉 worker pool。
// 所有執行中的任務會被中斷，佇列中的任務會被拋棄。
// 僅建議在緊急情況下使用，可以安全地重複呼叫。
func (p *Pool[R]) ShutdownNow() {
	p.closeOnce.Do(func() {
		close(p.done)
	})
}

// Resize 動態調整執行中 worker 的數量。
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

// Workers 回傳當前設定的 worker 數量。
func (p *Pool[R]) Workers() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.workers
}

// Stats 回傳 pool 當前的各項指標快照。
func (p *Pool[R]) Stats() Stats {
	return Stats{
		Pending:   p.pendingJobs.Load(),
		Completed: p.completedJobs.Load(),
		Failed:    p.failedJobs.Load(),
	}
}

// Results 回傳用於接收任務結果的 channel。
func (p *Pool[R]) Results() <-chan Result[R] {
	return p.results
}

// Submit 提交一個任務到 pool 中執行。如果佇列已滿，此方法會阻塞。
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

// TrySubmit 嘗試提交一個任務，如果佇列已滿或 pool 已關閉則不阻塞並立即回傳 false。
func (p *Pool[R]) TrySubmit(job Job[R]) bool {
	if job.Execute == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	case p.jobs <- job:
		p.pendingJobs.Add(1)
		return true
	default:
		return false
	}
}
