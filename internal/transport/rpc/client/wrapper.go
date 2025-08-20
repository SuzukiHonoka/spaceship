package client

import (
	"log"
	"math"
	"sync/atomic"

	"google.golang.org/grpc"
)

type ConnWrapper struct {
	*grpc.ClientConn
	InUse uint32
}

func NewConnWrapper(p *Params) (*ConnWrapper, error) {
	conn, err := grpc.NewClient(p.Addr, p.Opts...)
	if err != nil {
		return nil, err
	}
	wrapper := &ConnWrapper{
		ClientConn: conn,
	}
	return wrapper, nil
}

func (w *ConnWrapper) Use() {
	atomic.AddUint32(&w.InUse, 1)
}

func (w *ConnWrapper) Done() error {
	atomic.AddUint32(&w.InUse, ^uint32(0))
	return nil
}

func (w *ConnWrapper) Close() error {
	if w.ClientConn != nil {
		return w.ClientConn.Close()
	}
	return nil
}

type ConnWrappers []*ConnWrapper

func (w ConnWrappers) PickLRU() *ConnWrapper {
	var conn *ConnWrapper
	for lru, i := uint32(math.MaxUint32), 0; i < len(w); i++ {
		if wrapper := w[i]; wrapper.InUse < lru {
			lru = wrapper.InUse
			conn = wrapper
		}
	}
	return conn
}

func (w ConnWrappers) LogStatus() {
	inuse := make([]uint32, len(w))
	for i, wrapper := range w {
		inuse[i] = wrapper.InUse
	}
	log.Printf("Inuse status: %v", inuse)
}
