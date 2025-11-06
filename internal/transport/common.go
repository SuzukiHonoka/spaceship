package transport

import "time"

// BufferSize 1024K (1K == 1024 Byte)
var BufferSize = 1024 * 1024

// Network is a tcp dial option
var Network = "tcp"

// IdleTimeout for transport of direct
var IdleTimeout = 30 * time.Minute

// DialTimeout for transport of direct
var DialTimeout = 3 * time.Minute
