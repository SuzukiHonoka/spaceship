package client

import (
	"fmt"
	"log"
	"sync"
	"time"

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
	// Only connection creation is serialized; healthy checkouts stay on mu.
	dialMu      sync.Mutex
	idleSince   map[*ConnWrapper]time.Time
	retireTimer *time.Timer
	retireAfter time.Duration // zero selects the one-minute surplus grace period

	// Test seams also make the two independent safety limits explicit. Zero
	// selects the production defaults.
	maxLoadPerConnection     uint32
	maxPersistentConnections int
	newWrapper               func() (*ConnWrapper, error) // test/benchmark connection factory
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
	conn.ID = q.nextIDLocked()
	conn.surplus = len(q.Conn) >= q.Size
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
	if q.newWrapper != nil {
		return q.newWrapper()
	}
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
	if q.retireTimer != nil {
		q.retireTimer.Stop()
		q.retireTimer = nil
	}
	q.idleSince = nil
	for _, conn := range q.Conn {
		if conn != nil {
			utils.Close(conn)
		}
	}
}

// GetConnOutSide gets a connection outside the pool
func (q *ConnQueue) GetConnOutSide() (*ConnWrapper, func() error, error) {
	q.mu.Lock()
	if q.shutdown {
		q.mu.Unlock()
		return nil, nil, fmt.Errorf("connection queue is shutdown")
	}
	q.mu.Unlock()
	return q.dialOutside()
}

func (q *ConnQueue) dialOutside() (*ConnWrapper, func() error, error) {
	conn, err := q.Dial()
	if err != nil {
		return nil, nil, err
	}
	q.mu.RLock()
	closed := q.shutdown
	q.mu.RUnlock()
	if closed {
		utils.Close(conn)
		return nil, nil, fmt.Errorf("connection queue is shutdown")
	}
	return conn, onceError(conn.Close), nil
}

// GetConn gets a grpc connection from the pool, also moves the cursor
func (q *ConnQueue) GetConn() (*ConnWrapper, func() error, error) {
	slowHeld := false
	defer func() {
		if slowHeld {
			q.dialMu.Unlock()
		}
	}()
retry:
	// Selection and load reservation must be exclusive, but NewClient/Connect
	// must not run under q.mu: unpooled dials and elastic growth would otherwise
	// serialise every checkout behind control-plane setup.
	q.mu.Lock()
	if q.shutdown {
		q.mu.Unlock()
		return nil, nil, fmt.Errorf("connection queue is shutdown")
	}
	if q.Size == 0 {
		q.mu.Unlock()
		return q.dialOutside()
	}

	el := q.Conn.PickLeastLoaded()
	if el == nil {
		if !slowHeld {
			q.mu.Unlock()
			q.dialMu.Lock()
			slowHeld = true
			goto retry // capacity may have changed while waiting for the builder
		}
		replaceIndex, replaceID := q.firstShutdownIndexLocked()
		if replaceIndex < 0 {
			q.mu.Unlock()
			return nil, nil, fmt.Errorf("no available connections in pool")
		}
		q.mu.Unlock()
		replacement, err := q.Dial()
		if err != nil {
			return nil, nil, fmt.Errorf("replace unavailable connection %d: %w", replaceID, err)
		}
		q.mu.Lock()
		if q.shutdown {
			q.mu.Unlock()
			utils.Close(replacement)
			return nil, nil, fmt.Errorf("connection queue is shutdown")
		}
		el = q.installReplacementLocked(replaceIndex, replaceID, replacement)
		if el == nil {
			// Another goroutine repaired the slot; prefer any live wrapper.
			el = q.Conn.PickLeastLoaded()
			if el == nil {
				q.mu.Unlock()
				return nil, nil, fmt.Errorf("no available connections in pool")
			}
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
		if !slowHeld {
			q.mu.Unlock()
			q.dialMu.Lock()
			slowHeld = true
			goto retry
		}
		q.mu.Unlock()
		grown, err := q.Dial()
		if err != nil {
			return nil, nil, fmt.Errorf("grow connection pool: %w", err)
		}
		q.mu.Lock()
		if q.shutdown {
			q.mu.Unlock()
			utils.Close(grown)
			return nil, nil, fmt.Errorf("connection queue is shutdown")
		}
		if existing := q.Conn.PickLeastLoaded(); existing != nil &&
			existing.GetCurrentLoad() < q.streamLimit() {
			// Capacity freed or another grower landed while we dialled.
			utils.Close(grown)
			el = existing
		} else if len(q.Conn) >= q.persistentLimit() {
			q.mu.Unlock()
			utils.Close(grown)
			return nil, nil, fmt.Errorf(
				"connection pool capacity exhausted: %d connections at %d streams each",
				len(q.Conn),
				q.streamLimit(),
			)
		} else {
			grown.ID = q.nextIDLocked()
			grown.surplus = true
			q.Conn = append(q.Conn, grown)
			el = grown
		}
	}

	el.Use()
	if el.surplus {
		delete(q.idleSince, el)
	}
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

	if !el.surplus {
		return el, onceError(el.Done), nil
	}
	return el, onceError(func() error { return q.release(el) }), nil
}

func (q *ConnQueue) nextIDLocked() int {
	id := 0
	for _, conn := range q.Conn {
		if conn != nil && conn.ID > id {
			id = conn.ID
		}
	}
	return id + 1
}

func (q *ConnQueue) release(conn *ConnWrapper) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := conn.Done(); err != nil {
		return err
	}
	if q.shutdown || conn.GetCurrentLoad() != 0 {
		return nil
	}
	for i := q.Size; i < len(q.Conn); i++ {
		if q.Conn[i] != conn {
			continue
		}
		if q.idleSince == nil {
			q.idleSince = make(map[*ConnWrapper]time.Time)
		}
		q.idleSince[conn] = time.Now()
		q.scheduleRetirementLocked()
		break
	}
	return nil
}

func (q *ConnQueue) scheduleRetirementLocked() {
	if q.shutdown || q.retireTimer != nil || len(q.idleSince) == 0 {
		return
	}
	grace := q.retireAfter
	if grace <= 0 {
		grace = time.Minute
	}
	q.retireTimer = time.AfterFunc(grace, q.retireIdle)
}

// retireIdle removes only surplus zero-load wrappers under the same lock that
// reserves stream capacity. A live checkout can never race with retirement.
func (q *ConnQueue) retireIdle() {
	q.mu.Lock()
	q.retireTimer = nil
	if q.shutdown {
		q.mu.Unlock()
		return
	}
	grace := q.retireAfter
	if grace <= 0 {
		grace = time.Minute
	}
	var retired []*ConnWrapper
	for i := q.Size; i < len(q.Conn); {
		conn := q.Conn[i]
		since, idle := q.idleSince[conn]
		if !idle || conn.GetCurrentLoad() != 0 || time.Since(since) < grace {
			i++
			continue
		}
		retired = append(retired, conn)
		delete(q.idleSince, conn)
		copy(q.Conn[i:], q.Conn[i+1:])
		q.Conn[len(q.Conn)-1] = nil
		q.Conn = q.Conn[:len(q.Conn)-1]
	}
	q.scheduleRetirementLocked()
	q.mu.Unlock()
	for _, conn := range retired {
		utils.Close(conn)
	}
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

// firstShutdownIndexLocked finds a permanently closed pool slot to repair.
// q.mu must be held by the caller. replaceID is the 1-based connection ID.
func (q *ConnQueue) firstShutdownIndexLocked() (index int, replaceID int) {
	for index, old := range q.Conn {
		if old != nil && old.getState() != connectivity.Shutdown {
			continue
		}
		replaceID = index + 1
		if old != nil && old.ID > 0 {
			replaceID = old.ID
		}
		return index, replaceID
	}
	return -1, 0
}

// installReplacementLocked places a dialled wrapper into a shutdown slot when
// that slot still needs repair. Returns the live wrapper to use, which may be
// an existing one if another goroutine won the race. q.mu must be held.
func (q *ConnQueue) installReplacementLocked(
	index int,
	replaceID int,
	replacement *ConnWrapper,
) *ConnWrapper {
	if index < 0 || index >= len(q.Conn) {
		utils.Close(replacement)
		return nil
	}
	old := q.Conn[index]
	if old != nil && old.getState() != connectivity.Shutdown {
		utils.Close(replacement)
		return old
	}
	replacement.ID = replaceID
	replacement.surplus = index >= q.Size
	q.Conn[index] = replacement
	delete(q.idleSince, old)
	if old != nil {
		utils.Close(old)
	}
	return replacement
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
	q.dialMu.Lock()
	defer q.dialMu.Unlock()
	q.mu.RLock()
	needed := false
	if !q.shutdown {
		for _, conn := range q.Conn {
			if conn == old {
				needed = true
				break
			}
		}
	}
	q.mu.RUnlock()
	if !needed {
		return
	}
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
			delete(q.idleSince, old)
			newConn.ID = old.ID
			newConn.surplus = old.surplus
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
