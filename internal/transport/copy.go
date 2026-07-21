package transport

import (
	"context"
	"io"
)

type closeWriter interface {
	CloseWrite() error
}

// CopyWithContext copies from src to dst using the shared transport buffer pool.
// If ctx is canceled before the copy completes, unblock is called before waiting
// for the copy goroutine to exit.
func CopyWithContext(ctx context.Context, unblock func(), dst io.Writer, src io.Reader, direction Direction) error {
	buf := Buffer()
	defer PutBuffer(buf)

	type copyResult struct {
		n   int64
		err error
	}
	resultCh := make(chan copyResult, 1)
	go func() {
		n, err := io.CopyBuffer(dst, src, *buf)
		resultCh <- copyResult{n: n, err: err}
	}()

	select {
	case result := <-resultCh:
		GlobalStats.Add(direction, result.n)
		return result.err
	case <-ctx.Done():
		if unblock != nil {
			unblock()
		}
		result := <-resultCh
		GlobalStats.Add(direction, result.n)
		return ctx.Err()
	}
}

// CloseWriteOrClose closes the write side of v when supported. If the
// connection type does not support half-close, it falls back to Close so the
// opposite copy direction does not block forever.
func CloseWriteOrClose(v any) {
	if cw, ok := v.(closeWriter); ok {
		_ = cw.CloseWrite()
		return
	}
	if closer, ok := v.(io.Closer); ok {
		_ = closer.Close()
	}
}

// CloseAll closes every value that implements io.Closer.
//
// Identical closer values (same interface dynamic type and pointer) are only
// closed once. HTTP CONNECT passes the same conn as both src and dst into
// Proxy, and without dedupe that would close the client socket twice.
func CloseAll(values ...any) {
	seen := make([]io.Closer, 0, len(values))
	for _, value := range values {
		closer, ok := value.(io.Closer)
		if !ok || closer == nil {
			continue
		}
		dup := false
		for _, prev := range seen {
			if prev == closer {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		seen = append(seen, closer)
		_ = closer.Close()
	}
}
