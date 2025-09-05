package client

import (
	"fmt"
	"log"

	proxy "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"google.golang.org/grpc/connectivity"
)

type ConnQueue struct {
	Params   *Params
	Size     int
	Conn     ConnWrappers
	shutdown bool
}

type ConnNode struct {
	Conn *ConnWrapper
	Next *ConnNode
}

func NewConnQueue(size int, params *Params) *ConnQueue {
	queue := &ConnQueue{
		Params: params,
		Size:   size,
		Conn:   make([]*ConnWrapper, 0, size),
	}
	return queue
}

func (q *ConnQueue) Add(conn *ConnWrapper) {
	q.Conn = append(q.Conn, conn)
}

func (q *ConnQueue) Init() error {
	for i := 0; i < q.Size; i++ {
		conn, err := q.Dial()
		if err != nil {
			return err
		}
		q.Add(conn)
	}
	log.Println("ConnQueue initialized")
	return nil
}

// Dial dials new grpc connection with saved params
func (q *ConnQueue) Dial() (*ConnWrapper, error) {
	w, err := NewConnWrapper(q.Params)
	if err != nil {
		return nil, fmt.Errorf("creating grpc wrapper: %w", err)
	}
	// connect immediately
	w.Connect()
	return w, nil
}

// Destroy force disconnect all the connections
func (q *ConnQueue) Destroy() {
	q.shutdown = true
	for i := 0; i < q.Size; i++ {
		conn := q.Conn[i]
		if conn != nil {
			utils.Close(conn)
		}
	}
}

// GetConnOutSide gets a connection outside the pool
func (q *ConnQueue) GetConnOutSide() (*ConnWrapper, func() error, error) {
	conn, err := q.Dial()
	if err != nil {
		return nil, nil, err
	}
	return conn, conn.Close, nil
}

// GetConn gets a grpc connection from the pool, also moves the cursor
func (q *ConnQueue) GetConn() (*ConnWrapper, func() error, error) {
	if q.Size == 0 {
		return q.GetConnOutSide()
	}
	el := q.Conn.PickLRU()
	el.Use()
	// check if conn ok
	switch el.GetState() {
	case connectivity.Connecting:
	case connectivity.Ready:
	default:
		log.Println("grpc connection down, attempting to reconnect..")
		el.ResetConnectBackoff()
	}
	return el, el.Done, nil
}

// GetClient gets a grpc client from connection
func (q *ConnQueue) GetClient() (proxy.ProxyClient, func() error, error) {
	conn, done, err := q.GetConn()
	if err != nil {
		return nil, nil, err
	}
	// Use dynamic proxy client for configurable service names
	dynamicClient := NewDynamicProxyClient(conn)
	return dynamicClient, done, nil
}
