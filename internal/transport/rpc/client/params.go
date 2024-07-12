package client

import "google.golang.org/grpc"

type Params struct {
	Addr string
	Opts []grpc.DialOption
}

func NewParams(addr string, opts ...grpc.DialOption) *Params {
	return &Params{
		Addr: addr,
		Opts: opts,
	}
}
