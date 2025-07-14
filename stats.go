package pool

// Stats 包含工作池操作的統計數據。
type Stats struct {
	Pending   int64
	Completed int64
	Failed    int64
}
