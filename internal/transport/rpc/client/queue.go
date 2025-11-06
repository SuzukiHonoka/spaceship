package client

import (
	"fmt"
	"log"
	"sync"

	proxy "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"google.golang.org/grpc/connectivity"
)

type ConnQueue struct {
	Params   *Params
	Size     int
	Conn     ConnWrappers
	shutdown bool
	mu       sync.RWMutex // Protect concurrent access
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
	conn.ID = len(q.Conn) + 1 // Assign sequential ID starting from 1
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
	q.mu.Lock()
	defer q.mu.Unlock()

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

	q.mu.RLock()
	if q.shutdown {
		q.mu.RUnlock()
		return nil, nil, fmt.Errorf("connection queue is shutdown")
	}
	el := q.Conn.PickLRU()
	q.mu.RUnlock()

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

// GetConnectionStatus returns detailed connection status string
func (q *ConnQueue) GetConnectionStatus() string {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if q.shutdown {
		return "Connection pool shutdown"
	}

	return q.Conn.GetDetailedStatus()
}

// GetConnectionSummary returns comprehensive connection statistics
func (q *ConnQueue) GetConnectionSummary() (total, active int, currentLoad uint32) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if q.shutdown {
		return 0, 0, 0
	}

	return q.Conn.GetSummaryStats()
}

// LogConnectionStatus logs the detailed connection status
func (q *ConnQueue) LogConnectionStatus() {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if q.shutdown {
		log.Println("gRPC Connection Pool: SHUTDOWN")
		return
	}

	total, active, currentLoad := q.Conn.GetSummaryStats()
	detailedStatus := q.Conn.GetDetailedStatus()

	log.Printf("gRPC Pool Status: %d total, %d active, %d current load",
		total, active, currentLoad)
	log.Printf("Connection Usage: %s", detailedStatus)
}
