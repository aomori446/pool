package pool

import (
	"context"
	"time"
)

// Job 代表一個將由工作池中的 worker 執行的工作單元。
type Job[R any] struct {
	Execute func(ctx context.Context) (R, error)
	Context context.Context
	Timeout time.Duration
	Retries int
	Backoff time.Duration
}
