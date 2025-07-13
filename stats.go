package pool

type Stats struct {
	Pending   int64
	Completed int64
	Failed    int64
}
