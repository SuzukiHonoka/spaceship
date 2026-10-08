package client

import (
	"context"
	"fmt"
	"log"
	"math"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

// ConnectionDetail holds individual connection information for web display
type ConnectionDetail struct {
	ID                int    `json:"id"`
	Load              uint32 `json:"load"`
	Status            string `json:"status"`             // active/idle based on load
	ConnectivityState string `json:"connectivity_state"` // gRPC connectivity state
	HealthStatus      string `json:"health_status"`      // derived health status
}

type ConnWrapper struct {
	*grpc.ClientConn
	// Set before publication and immutable thereafter. Warm wrappers need no
	// idle-retirement bookkeeping on the hot release path.
	surplus bool
	ID      int           // Connection ID for display
	InUse   atomic.Uint32 // How many external connections are currently using this gRPC connection

	// lastActive is when a reservation last started or ended (Unix nanos), so
	// the keep-warm loop can tell configured channel idleness from a lost
	// transport.
	lastActive atomic.Int64
	// stopWarm ends the keep-warm loop. Set before publication.
	stopWarm context.CancelFunc
}

func NewConnWrapper(p *Params) (*ConnWrapper, error) {
	if p == nil {
		return nil, fmt.Errorf("rpc client: nil connection parameters")
	}
	target, err := controlTarget(p.Addr)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(target, p.Opts...)
	if err != nil {
		return nil, err
	}
	wrapper := &ConnWrapper{
		ClientConn: conn,
		// ID will be set by the queue when adding to pool
	}
	return wrapper, nil
}

// controlTarget forces grpc-go to pass the configured host:port unchanged to
// our context dialer. Using grpc-go's default DNS resolver would resolve the
// control-plane hostname through net.DefaultResolver before dialContext runs,
// bypassing both the configured resolver and Linux SO_MARK policy.
func controlTarget(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("rpc client: invalid server address %q: %w", address, err)
	}
	if host == "" {
		return "", fmt.Errorf("rpc client: invalid server address %q: empty host", address)
	}
	if port == "" {
		return "", fmt.Errorf("rpc client: invalid server address %q: empty port", address)
	}

	return (&url.URL{Scheme: "passthrough", Path: "/" + address}).String(), nil
}

func (w *ConnWrapper) Use() {
	w.InUse.Add(1)
	w.touch()
}

func (w *ConnWrapper) touch() {
	w.lastActive.Store(time.Now().UnixNano())
}

func (w *ConnWrapper) Done() error {
	for {
		current := w.InUse.Load()
		if current == 0 {
			return fmt.Errorf("rpc connection %d has no reserved usage", w.ID)
		}
		if w.InUse.CompareAndSwap(current, current-1) {
			w.touch()
			return nil
		}
	}
}

// GetCurrentLoad returns the current number of external connections using this gRPC connection
func (w *ConnWrapper) GetCurrentLoad() uint32 {
	return w.InUse.Load()
}

func (w *ConnWrapper) getState() connectivity.State {
	if w == nil || w.ClientConn == nil {
		return connectivity.Shutdown
	}
	return w.GetState()
}

func (w *ConnWrapper) Close() error {
	if w.stopWarm != nil {
		w.stopWarm()
	}
	if w.ClientConn != nil {
		return w.ClientConn.Close()
	}
	return nil
}

type ConnWrappers []*ConnWrapper

// PickLeastLoaded returns the least-loaded usable connection, preferring
// ready transports. See pick.
func (w ConnWrappers) PickLeastLoaded() *ConnWrapper {
	return w.pick(math.MaxUint32)
}

// pickTier orders connectivity states by how soon a new stream can start: a
// ready transport at once, an idle or connecting one only after a TCP, TLS and
// HTTP/2 handshake, and one in transient failure not at all (a fail-fast RPC
// fails immediately). A shut-down connection is never usable.
func pickTier(state connectivity.State) int {
	switch state {
	case connectivity.Ready:
		return 0
	case connectivity.Idle, connectivity.Connecting:
		return 1
	case connectivity.TransientFailure:
		return 2
	default:
		return -1
	}
}

// pick returns the least-loaded connection from the best connectivity tier
// that still has stream capacity below limit. Least load alone would favour a
// connection whose transport was just reset: its sessions died with it, so it
// reads as the emptiest, and the next session would pay its reconnect while
// warm connections sit unused. When every usable connection is at limit, pick
// returns the least-loaded one from the best tier so the caller can decide to
// grow the pool.
func (w ConnWrappers) pick(limit uint32) *ConnWrapper {
	const tiers = 3
	var (
		withCapacity [tiers]*ConnWrapper
		anyLoad      [tiers]*ConnWrapper
	)
	for _, c := range w {
		if c == nil {
			continue
		}
		tier := pickTier(c.getState())
		if tier < 0 {
			continue
		}
		load := c.InUse.Load()
		if best := anyLoad[tier]; best == nil || load < best.InUse.Load() {
			anyLoad[tier] = c
		}
		if load >= limit {
			continue
		}
		if best := withCapacity[tier]; best == nil || load < best.InUse.Load() {
			withCapacity[tier] = c
		}
	}
	for _, c := range withCapacity {
		if c != nil {
			return c
		}
	}
	for _, c := range anyLoad {
		if c != nil {
			return c
		}
	}
	return nil
}

func (w ConnWrappers) LogStatus() {
	inuse := make([]uint32, 0, len(w))
	for _, wrapper := range w {
		if wrapper != nil {
			inuse = append(inuse, wrapper.InUse.Load())
		}
	}
	log.Printf("Inuse status: %v", inuse)
}

// GetDetailedStatus returns comprehensive status string like "1(10) 2(11) 3(5)"
func (w ConnWrappers) GetDetailedStatus() string {
	if len(w) == 0 {
		return "No connections"
	}

	var sb strings.Builder
	sb.Grow(len(w) * 10) // estimate ~10 chars per connection
	written := 0
	for _, wrapper := range w {
		if wrapper == nil {
			continue
		}
		if written > 0 {
			sb.WriteByte(' ')
		}
		currentLoad := wrapper.GetCurrentLoad()
		_, _ = fmt.Fprintf(&sb, "%d(%d)", wrapper.ID, currentLoad)
		written++
	}
	if written == 0 {
		return "No connections"
	}
	return sb.String()
}

// GetSummaryStats returns pool summary statistics
func (w ConnWrappers) GetSummaryStats() (total int, active int, totalLoad uint32) {
	for _, wrapper := range w {
		if wrapper == nil {
			continue
		}
		total++
		currentLoad := wrapper.GetCurrentLoad()
		if currentLoad > 0 {
			active++
		}
		totalLoad += currentLoad
	}
	return
}

// GetConnectionDetails returns individual connection information for web display
func (w ConnWrappers) GetConnectionDetails() []ConnectionDetail {
	details := make([]ConnectionDetail, 0, len(w))
	for _, wrapper := range w {
		if wrapper == nil {
			continue
		}
		load := wrapper.GetCurrentLoad()

		// Activity status based on load
		status := "idle"
		if load > 0 {
			status = "active"
		}

		// Get real gRPC connectivity state
		grpcState := wrapper.getState()
		connectivityState := grpcStateToString(grpcState)

		// Derive health status from gRPC state and load
		healthStatus := deriveHealthStatus(grpcState, load)

		details = append(details, ConnectionDetail{
			ID:                wrapper.ID,
			Load:              load,
			Status:            status,
			ConnectivityState: connectivityState,
			HealthStatus:      healthStatus,
		})
	}
	return details
}

// grpcStateToString converts gRPC connectivity state to human-readable string
func grpcStateToString(state connectivity.State) string {
	switch state {
	case connectivity.Idle:
		return "IDLE"
	case connectivity.Connecting:
		return "CONNECTING"
	case connectivity.Ready:
		return "READY"
	case connectivity.TransientFailure:
		return "TRANSIENT_FAILURE"
	case connectivity.Shutdown:
		return "SHUTDOWN"
	default:
		return "UNKNOWN"
	}
}

// deriveHealthStatus determines overall health from gRPC state and current load
func deriveHealthStatus(state connectivity.State, load uint32) string {
	switch state {
	case connectivity.Ready:
		if load > 0 {
			return "healthy_active"
		}
		return "healthy_ready"
	case connectivity.Idle:
		return "healthy_idle"
	case connectivity.Connecting:
		return "connecting"
	case connectivity.TransientFailure:
		return "unhealthy"
	case connectivity.Shutdown:
		return "shutdown"
	default:
		return "unknown"
	}
}
