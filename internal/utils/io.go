package utils

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
)

// Close closes closer and logs unexpected errors.
//
// Preferred fix for shared client conns is OnceNetConn (idempotent Close) so
// front ends and transports can both own teardown without racing. This helper
// still treats already-closed / peer-reset results as quiet: they remain
// possible from the peer side or older call paths.
func Close(closer io.Closer) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil && !isBenignCloseError(err) {
		// %T avoids dumping opaque conn internals (&{{0x...}}).
		log.Printf("close %T failed: %v", closer, err)
	}
}

// isBenignCloseError reports close results that are expected during normal
// connection teardown and should not be logged.
func isBenignCloseError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, os.ErrClosed) {
		return true
	}
	// Older stacks / wrappers may not wrap net.ErrClosed / syscall errors.
	msg := err.Error()
	return strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "broken pipe")
}

func PrettyByteSize(b float64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%.0fB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	// Use slightly more compact output for single line (e.g., no space before unit)
	return fmt.Sprintf("%.2f%ciB", b/float64(div), "KMGTPEZ"[exp])
}
