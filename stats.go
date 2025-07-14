package pool

import "sync/atomic"

// Stats 包含工作池操作的統計數據。
type Stats struct {
	pending   atomic.Int64
	completed atomic.Int64
	failed    atomic.Int64
}
