package client

import (
	proxy "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"log"
	"sync"
)

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

// Pool is a looped sequence, stores the grpc connections for reuse propose
type Pool struct {
	Position int          // current cluster position
	Size     int          // max capacity
	Elements ConnWrappers // connections
	Params   *Params
	sync.Mutex
}

// NewPool returns a new pool instance with fixed size
func NewPool(size int, addr string, opts ...grpc.DialOption) *Pool {
	return &Pool{
		Size:     size,
		Params:   NewParams(addr, opts...),
		Position: 1,
	}
}

// Dial dials new grpc connection with saved params
func (p *Pool) Dial() (*ConnWrapper, error) {
	conn, err := grpc.Dial(p.Params.Addr, p.Params.Opts...)
	if err != nil {
		return nil, err
	}
	return &ConnWrapper{
		ClientConn: conn,
	}, nil
}

// Init initialize the pool connection at once instead on-demand
func (p *Pool) Init() error {
	if p.Size == 0 {
		log.Println("grpc: dial on demand mode")
		return nil
	}
	log.Printf("grpc: connection pool size: %d", p.Size)
	p.Elements = make(ConnWrappers, p.Size)
	for i := 0; i < p.Size; i++ {
		conn, err := p.Dial()
		if err != nil {
			p.Destroy()
			return err
		}
		p.Elements[i] = conn
	}
	return nil
}

// Destroy force disconnect all the connections
func (p *Pool) Destroy() {
	for _, conn := range p.Elements {
		if conn.ClientConn != nil {
			_ = conn.Close()
		}
	}
}

// GetConnOutSide gets a connection outside the pool
func (p *Pool) GetConnOutSide() (*ConnWrapper, func() error, error) {
	conn, err := p.Dial()
	if err != nil {
		return nil, nil, err
	}
	return conn, conn.Close, nil
}

// GetConn gets a grpc connection from the pool, also moves the cluster
func (p *Pool) GetConn() (*ConnWrapper, func() error, error) {
	// no mux
	if p.Size == 0 {
		return p.GetConnOutSide()
	}
	var el *ConnWrapper
	// mux
	p.Lock()
	// get el from actual position inside the slice
	el = p.Elements[p.Position]
	// move cluster
	p.Position++
	// check if overflow
	if p.Position == p.Size {
		// looped sequence: start over
		p.Position = 1
	}
	p.Unlock()
	if el == nil {
		return p.GetConnOutSide()
		//return nil, nil, fmt.Errorf("connection not initialized at position %d %w", p.Position, transport.ErrorNotInitialized)
	}
	el.Use()
	// check if conn ok
	switch el.GetState() {
	case connectivity.Connecting:
	case connectivity.Ready:
	default:
		log.Printf("grpc connection down, attempting to reconnect..")
		el.ResetConnectBackoff()
	}
	return el, el.Done, nil
}

// GetClient gets a grpc client from connection
func (p *Pool) GetClient() (proxy.ProxyClient, func() error, error) {
	conn, done, err := p.GetConn()
	if err != nil {
		return nil, nil, err
	}
	return proxy.NewProxyClient(conn), done, nil
}
