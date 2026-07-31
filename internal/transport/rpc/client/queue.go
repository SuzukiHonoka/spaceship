package client

import (
	"fmt"
	"log"
	"sync"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	proxy "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"google.golang.org/grpc/connectivity"
)

// MaxPersistentConnections matches the uint8 mux configuration ceiling. An
// elastic queue never grows beyond this process-local resource boundary.
const MaxPersistentConnections = 255

type ConnQueue struct {
	Params   *Params
	Size     int
	Conn     ConnWrappers
	shutdown bool
	mu       sync.RWMutex // Protect concurrent access.

	// Test seams also make the two independent safety limits explicit. Zero
	// selects the production defaults.
	maxLoadPerConnection     uint32
	maxPersistentConnections int
}

func NewConnQueue(size int, params *Params) *ConnQueue {
	capacity := max(size, 0)
	queue := &ConnQueue{
		Params: params,
		Size:   size,
		Conn:   make([]*ConnWrapper, 0, capacity),
	}
	return queue
}

func (q *ConnQueue) Add(conn *ConnWrapper) {
	if conn == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.shutdown {
		utils.Close(conn)
		return
	}
	conn.ID = len(q.Conn) + 1 // Assign sequential ID starting from 1
	q.Conn = append(q.Conn, conn)
}

func (q *ConnQueue) Init() error {
	if q.Size < 0 || q.Size > q.persistentLimit() {
		return fmt.Errorf(
			"connection queue size must be between 0 and %d: %d",
			q.persistentLimit(),
			q.Size,
		)
	}
	for i := 0; i < q.Size; i++ {
		conn, err := q.Dial()
		if err != nil {
			// A future dial implementation may fail after earlier wrappers were
			// created. Close the partial pool now rather than leaking control
			// connections from a failed client initialization.
			q.Destroy()
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
	for _, conn := range q.Conn {
		if conn != nil {
			utils.Close(conn)
		}
	}
}

// GetConnOutSide gets a connection outside the pool
func (q *ConnQueue) GetConnOutSide() (*ConnWrapper, func() error, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.shutdown {
		return nil, nil, fmt.Errorf("connection queue is shutdown")
	}
	return q.getConnOutsideLocked()
}

func (q *ConnQueue) getConnOutsideLocked() (*ConnWrapper, func() error, error) {
	conn, err := q.Dial()
	if err != nil {
		return nil, nil, err
	}
	return conn, onceError(conn.Close), nil
}

// GetConn gets a grpc connection from the pool, also moves the cursor
func (q *ConnQueue) GetConn() (*ConnWrapper, func() error, error) {
	// Selection and load reservation must be one exclusive operation. With a
	// shared read lock (or a reservation after unlocking), a burst of callers
	// can all observe the same least-loaded connection and exceed its HTTP/2
	// stream limit while the rest of the pool remains idle.
	q.mu.Lock()
	if q.shutdown {
		q.mu.Unlock()
		return nil, nil, fmt.Errorf("connection queue is shutdown")
	}
	if q.Size == 0 {
		conn, done, err := q.getConnOutsideLocked()
		q.mu.Unlock()
		return conn, done, err
	}

	el := q.Conn.PickLeastLoaded()
	if el == nil {
		var err error
		el, err = q.replaceFirstShutdownLocked()
		if err != nil {
			q.mu.Unlock()
			return nil, nil, err
		}
	}
	if el.GetCurrentLoad() >= q.streamLimit() {
		if len(q.Conn) >= q.persistentLimit() {
			q.mu.Unlock()
			return nil, nil, fmt.Errorf(
				"connection pool capacity exhausted: %d connections at %d streams each",
				len(q.Conn),
				q.streamLimit(),
			)
		}
		var err error
		el, err = q.dialPersistentLocked()
		if err != nil {
			q.mu.Unlock()
			return nil, nil, fmt.Errorf("grow connection pool: %w", err)
		}
	}
	el.Use()
	q.mu.Unlock()

	// Check the state after reserving load. If the connection became permanently
	// unavailable after selection, roll the reservation back before replacing it.
	switch el.getState() {
	case connectivity.Ready:
		// Connection is ready
	case connectivity.Connecting:
		// A fail-fast call waits while the initial connection attempt is still
		// in progress, then either starts or returns that attempt's error.
	case connectivity.Idle:
		// Trigger connection from idle state
		el.Connect()
	case connectivity.TransientFailure:
		// Every non-degraded wrapper was unavailable. Preserve grpc-go's
		// exponential reconnect backoff; resetting it for every inbound request
		// would turn an outage into a connection-attempt storm. The fail-fast RPC
		// reports the current connection error.
	case connectivity.Shutdown:
		// Connection is permanently dead, replace it in the pool
		_ = el.Done()
		go q.replaceConn(el)
		return nil, nil, fmt.Errorf("grpc connection %d is shutdown", el.ID)
	}

	return el, onceError(el.Done), nil
}

func onceError(fn func() error) func() error {
	var (
		once sync.Once
		err  error
	)
	return func() error {
		once.Do(func() {
			err = fn()
		})
		return err
	}
}

func (q *ConnQueue) streamLimit() uint32 {
	if q.maxLoadPerConnection > 0 {
		return q.maxLoadPerConnection
	}
	return rpc.MaxConcurrentStreams
}

func (q *ConnQueue) persistentLimit() int {
	if q.maxPersistentConnections > 0 {
		return q.maxPersistentConnections
	}
	return MaxPersistentConnections
}

// dialPersistentLocked adds one live wrapper to an elastic persistent queue.
// q.mu must be held by the caller.
func (q *ConnQueue) dialPersistentLocked() (*ConnWrapper, error) {
	conn, err := q.Dial()
	if err != nil {
		return nil, err
	}
	conn.ID = len(q.Conn) + 1
	q.Conn = append(q.Conn, conn)
	return conn, nil
}

// replaceFirstShutdownLocked synchronously repairs a pool whose every wrapper
// is permanently closed. q.mu must be held by the caller.
func (q *ConnQueue) replaceFirstShutdownLocked() (*ConnWrapper, error) {
	for index, old := range q.Conn {
		if old != nil && old.getState() != connectivity.Shutdown {
			continue
		}
		replacement, err := q.Dial()
		if err != nil {
			return nil, fmt.Errorf("replace unavailable connection %d: %w", index+1, err)
		}
		replacement.ID = index + 1
		if old != nil && old.ID > 0 {
			replacement.ID = old.ID
		}
		q.Conn[index] = replacement
		if old != nil {
			utils.Close(old)
		}
		return replacement, nil
	}
	return nil, fmt.Errorf("no available connections in pool")
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

// replaceConn replaces a shutdown connection with a new one in the pool.
func (q *ConnQueue) replaceConn(old *ConnWrapper) {
	newConn, err := q.Dial()
	if err != nil {
		log.Printf("rpc: replace connection %d failed: %v", old.ID, err)
		return
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.shutdown {
		utils.Close(newConn)
		return
	}

	for i, conn := range q.Conn {
		if conn == old {
			newConn.ID = old.ID
			q.Conn[i] = newConn
			utils.Close(old)
			log.Printf("replaced shutdown connection %d with new connection", old.ID)
			return
		}
	}
	// Already replaced by another goroutine
	utils.Close(newConn)
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

// GetConnectionDetails returns individual connection information for web display
func (q *ConnQueue) GetConnectionDetails() []ConnectionDetail {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if q.shutdown {
		return nil
	}

	return q.Conn.GetConnectionDetails()
}
