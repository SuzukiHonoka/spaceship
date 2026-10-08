package client

import (
	"time"

	"google.golang.org/grpc"
)

type Params struct {
	Addr string
	Opts []grpc.DialOption
	// IdleTimeout mirrors the channel idle timeout in Opts (zero: disabled),
	// so keeping pooled connections warm does not override it.
	IdleTimeout time.Duration
}

func NewParams(addr string, opts ...grpc.DialOption) *Params {
	return &Params{
		Addr: addr,
		Opts: opts,
	}
}
