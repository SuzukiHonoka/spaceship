package client

import (
	"google.golang.org/grpc"
	"sync/atomic"
)

type ConnWrapper struct {
	*grpc.ClientConn
	InUse uint32
}

func (w *ConnWrapper) Use() {
	atomic.AddUint32(&w.InUse, 1)
}

func (w *ConnWrapper) Done() error {
	atomic.AddUint32(&w.InUse, ^uint32(0))
	return nil
}

type ConnWrappers []*ConnWrapper
