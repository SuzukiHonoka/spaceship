package client

import (
	"google.golang.org/grpc"
	"log"
	"math"
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

func (w ConnWrappers) PickLRU() *ConnWrapper {
	lru := uint32(math.MaxUint32)
	var conn *ConnWrapper
	for i := 0; i < len(w); i++ {
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
