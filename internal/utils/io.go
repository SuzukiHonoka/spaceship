package utils

import (
	"io"
	"log"
)

// Close closes the closer
func Close(closer io.Closer) {
	if err := closer.Close(); err != nil {
		log.Printf("closer: %v close failed, err=%s", closer, err)
	}
}
