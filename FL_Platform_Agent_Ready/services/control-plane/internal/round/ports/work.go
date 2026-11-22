package ports

// Work is the persisted, frozen aggregation claim, never a mutable run cache.
type Work struct {
	RunID, RoundID, Owner, Schema string
	Round                         int
	Epoch                         int64
	Updates                       []string
}
type Input struct {
	Key, Hash, Schema string
	Size              int64
	Count             uint64
}
