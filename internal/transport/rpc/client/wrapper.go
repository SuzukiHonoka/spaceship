package client

import (
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"sync/atomic"

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
	ID    int           // Connection ID for display
	InUse atomic.Uint32 // How many external connections are currently using this gRPC connection
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
}

func (w *ConnWrapper) Done() error {
	for {
		current := w.InUse.Load()
		if current == 0 {
			return fmt.Errorf("rpc connection %d has no reserved usage", w.ID)
		}
		if w.InUse.CompareAndSwap(current, current-1) {
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
	return w.ClientConn.GetState()
}

func (w *ConnWrapper) Close() error {
	if w.ClientConn != nil {
		return w.ClientConn.Close()
	}
	return nil
}

type ConnWrappers []*ConnWrapper

func (w ConnWrappers) PickLeastLoaded() *ConnWrapper {
	if len(w) == 0 {
		return nil
	}

	var (
		conn             *ConnWrapper
		minUsage         uint32
		degraded         *ConnWrapper
		minDegradedUsage uint32
	)

	for _, c := range w {
		// Skip permanently dead connections so they are never chosen.
		// replaceConn() will swap them out asynchronously.
		if c == nil {
			continue
		}
		state := c.getState()
		if state == connectivity.Shutdown {
			continue
		}
		load := c.InUse.Load()
		// A fail-fast RPC sent through TransientFailure fails immediately. Keep
		// such a wrapper only as a fallback when every live connection is
		// degraded; otherwise one broken, idle wrapper can mask healthy peers.
		if state == connectivity.TransientFailure {
			if degraded == nil || load < minDegradedUsage {
				minDegradedUsage = load
				degraded = c
			}
			continue
		}
		if conn == nil || load < minUsage {
			minUsage = load
			conn = c
		}
	}
	if conn == nil {
		return degraded
	}
	return conn
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
