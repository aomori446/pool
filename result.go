package pool

// Result 包含工作執行的結果。
type Result[V any] struct {
	Error error
	Value V
}
