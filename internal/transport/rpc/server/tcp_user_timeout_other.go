//go:build !linux

package server

import (
	"net"
	"time"
)

func setTCPUserTimeout(net.Conn, time.Duration) error {
	return nil
}
