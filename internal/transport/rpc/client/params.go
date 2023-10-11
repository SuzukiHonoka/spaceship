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

func (p *Params) Dial() (*ConnWrapper, error) {
	conn, err := grpc.Dial(p.Addr, p.Opts...)
	if err != nil {
		return nil, err
	}
	return &ConnWrapper{
		ClientConn: conn,
	}, nil
}
