package rpc

import (
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"log"
	"spaceship/internal/transport"
	proxy "spaceship/internal/transport/rpc/proto"
	"sync"
)

// Pool is a looped sequence, stores the grpc connections for reuse propose
type Pool struct {
	Position int                // current cluster position
	Size     int                // max capacity
	Elements []*grpc.ClientConn // connections
	sync.Mutex
}

// NewPool returns a new pool instance with fixed size
func NewPool(size int) *Pool {
	if size == 0 {
		size = 1
	}
	return &Pool{
		Position: 0,
		Size:     size,
		Elements: make([]*grpc.ClientConn, size),
	}
}

// FullInit initialize the pool connection at once instead on-demand
func (p *Pool) FullInit(addr string, opts ...grpc.DialOption) (err error) {
	log.Printf("grpc connection pool size: %d", p.Size)
	for i := 0; i < p.Size; i++ {
		p.Elements[i], err = grpc.Dial(addr, opts...)
		if err != nil {
			return err
		}
	}
	return nil
}

// Destroy force disconnect all the connections
func (p *Pool) Destroy() {
	for _, conn := range p.Elements {
		if conn != nil {
			_ = conn.Close()
		}
	}
}

// GetConn gets a grpc connection from the pool, also moves the cluster
func (p *Pool) GetConn() (*grpc.ClientConn, error) {
	// no mux
	var el *grpc.ClientConn
	if p.Size == 1 {
		el = p.Elements[0]
	} else {
		// mux
		p.Lock()
		// move cluster
		p.Position++
		// check if overflow
		if p.Position > p.Size {
			// looped sequence: start over
			p.Position = 1
		}
		// get el from actual position inside the slice
		el = p.Elements[p.Position-1]
		p.Unlock()
	}
	if el == nil {
		return nil, fmt.Errorf("connection not initialized at position %d %w", p.Position, transport.ErrorNotInitialized)
	}
	// check if conn ok
	switch el.GetState() {
	case connectivity.Connecting:
	case connectivity.Ready:
	default:
		log.Printf("grpc connection down, attempting to reconnect..")
		// reconnect
		el.ResetConnectBackoff()
	}
	return el, nil
}

// GetClient gets a grpc client from connection
func (p *Pool) GetClient() (proxy.ProxyClient, error) {
	conn, err := p.GetConn()
	if err != nil {
		return nil, err
	}
	return proxy.NewProxyClient(conn), nil
}
