package utils

import (
	"fmt"
	"io"
	"log"
)

// Close closes the closer
func Close(closer io.Closer) {
	if err := closer.Close(); err != nil {
		log.Printf("closer: %v close failed, err=%s", closer, err)
	}
}

func PrettyByteSize(b float64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%.0f B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	// Use slightly more compact output for single line (e.g., no space before unit)
	return fmt.Sprintf("%.2f%ciB", b/float64(div), "KMGTPEZ"[exp])
}
