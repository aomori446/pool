package pool

import (
	"context"
	"time"
)

type Job[R any] struct {
	Execute func(ctx context.Context) (R, error)
	Context context.Context
	Timeout time.Duration
	Retries int
	Backoff time.Duration
}
