package transport

import (
	"io"
	"reflect"
	"slices"
)

// Aborter is implemented by connections that can close abortively, so the
// peer observes a reset instead of an orderly end of stream.
type Aborter interface {
	Abort() error
}

type lingerSetter interface {
	SetLinger(sec int) error
}

// Abort closes v so its peer learns the stream ended abnormally.
//
// A proxy that relays an upstream failure as an orderly close (FIN) makes a
// truncated response indistinguishable from a complete one to any protocol
// framed by end of stream, and the client has no reason to retry. Abort sends
// a reset instead: values implementing Aborter decide how, a TCP connection
// gets SO_LINGER 0 before Close, and anything else is simply closed.
//
// Only abort a stream whose incoming data is already incomplete. A reset can
// discard data the peer has received but not yet read.
func Abort(v any) {
	switch c := v.(type) {
	case Aborter:
		_ = c.Abort()
	case lingerSetter:
		_ = c.SetLinger(0)
		if closer, ok := v.(io.Closer); ok {
			_ = closer.Close()
		}
	case io.Closer:
		_ = c.Close()
	}
}

// AbortAll aborts every distinct value. HTTP CONNECT passes one connection as
// both src and dst, so identical values are aborted once. Values whose
// dynamic type is not comparable (comparing them would panic) are never
// treated as duplicates.
func AbortAll(values ...any) {
	seen := make([]any, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		if reflect.TypeOf(value).Comparable() {
			if slices.Contains(seen, value) {
				continue
			}
			seen = append(seen, value)
		}
		Abort(value)
	}
}
