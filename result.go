package pool

type Result[V any] struct {
	Error error
	Value V
}
