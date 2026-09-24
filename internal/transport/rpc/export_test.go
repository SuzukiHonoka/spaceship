package rpc

// RetainedPayloadViewCount reports how many messages currently hold a
// zero-copy receive registration, so external end-to-end tests can assert
// that finished streams release every one of them.
func RetainedPayloadViewCount() int {
	n := 0
	heldBuffers.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}
