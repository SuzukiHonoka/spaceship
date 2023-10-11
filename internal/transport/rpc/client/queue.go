package client

import (
	proxy "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"google.golang.org/grpc/connectivity"
	"log"
)

type ConnQueue struct {
	Params   *Params
	Stream   chan *ConnWrapper
	Size     int
	Rear     *ConnNode
	shutdown bool
}

type ConnNode struct {
	Conn *ConnWrapper
	Next *ConnNode
}

func NewConnQueue(size int, params *Params) *ConnQueue {
	queue := &ConnQueue{
		Params: params,
		Stream: make(chan *ConnWrapper),
	}
	for i := 0; i < size; i++ {
		node := new(ConnNode)
		queue.Insert(node)
	}
	return queue
}

func (q *ConnQueue) Wheel() {
	for p := q.Rear; !q.shutdown; p = p.Next {
		q.Stream <- p.Conn
	}
}

func (q *ConnQueue) Insert(node *ConnNode) {
	// queue empty
	if q.Rear == nil {
		node.Next = node
		q.Rear = node
	} else {
		node.Next = q.Rear.Next
		q.Rear.Next = node
	}
	q.Size++
}

func (q *ConnQueue) Init() error {
	p := q.Rear
	for i := 0; i < q.Size; i++ {
		conn, err := q.Dial()
		if err != nil {
			return err
		}
		p.Conn = conn
		p = p.Next
	}
	go q.Wheel()
	return nil
}

// Dial dials new grpc connection with saved params
func (q *ConnQueue) Dial() (*ConnWrapper, error) {
	return q.Params.Dial()
}

// Destroy force disconnect all the connections
func (q *ConnQueue) Destroy() {
	q.shutdown = true
	p := q.Rear
	for i := 0; i < q.Size; i++ {
		if p.Conn != nil {
			utils.ForceClose(p.Conn)
		}
		p = p.Next
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
	el := <-q.Stream
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
	return proxy.NewProxyClient(conn), done, nil
}
