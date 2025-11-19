package client

import (
	"fmt"
	"log"
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
	ID    int    // Connection ID for display
	InUse uint32 // How many external connections are currently using this gRPC connection
}

func NewConnWrapper(p *Params) (*ConnWrapper, error) {
	conn, err := grpc.NewClient(p.Addr, p.Opts...)
	if err != nil {
		return nil, err
	}
	wrapper := &ConnWrapper{
		ClientConn: conn,
		// ID will be set by the queue when adding to pool
	}
	return wrapper, nil
}

func (w *ConnWrapper) Use() {
	atomic.AddUint32(&w.InUse, 1)
}

func (w *ConnWrapper) Done() error {
	atomic.AddUint32(&w.InUse, ^uint32(0))
	return nil
}

// GetCurrentLoad returns the current number of external connections using this gRPC connection
func (w *ConnWrapper) GetCurrentLoad() uint32 {
	return atomic.LoadUint32(&w.InUse)
}

func (w *ConnWrapper) Close() error {
	if w.ClientConn != nil {
		return w.ClientConn.Close()
	}
	return nil
}

type ConnWrappers []*ConnWrapper

func (w ConnWrappers) PickLRU() *ConnWrapper {
	if len(w) == 0 {
		return nil
	}

	// For small connection pools, linear search is fine and simple
	// For larger pools, this could be optimized with a heap or better data structure
	conn := w[0]
	minUsage := conn.InUse

	for i := 1; i < len(w); i++ {
		if w[i].InUse < minUsage {
			minUsage = w[i].InUse
			conn = w[i]
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

// GetDetailedStatus returns comprehensive status string like "1(10) 2(11) 3(5)"
func (w ConnWrappers) GetDetailedStatus() string {
	if len(w) == 0 {
		return "No connections"
	}

	status := ""
	for i, wrapper := range w {
		if i > 0 {
			status += " "
		}
		currentLoad := wrapper.GetCurrentLoad()
		status += fmt.Sprintf("%d(%d)", wrapper.ID, currentLoad)
	}
	return status
}

// GetSummaryStats returns pool summary statistics
func (w ConnWrappers) GetSummaryStats() (total int, active int, totalLoad uint32) {
	total = len(w)
	for _, wrapper := range w {
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
	details := make([]ConnectionDetail, len(w))
	for i, wrapper := range w {
		load := wrapper.GetCurrentLoad()

		// Activity status based on load
		status := "idle"
		if load > 0 {
			status = "active"
		}

		// Get real gRPC connectivity state
		grpcState := wrapper.ClientConn.GetState()
		connectivityState := grpcStateToString(grpcState)

		// Derive health status from gRPC state and load
		healthStatus := deriveHealthStatus(grpcState, load)

		details[i] = ConnectionDetail{
			ID:                wrapper.ID,
			Load:              load,
			Status:            status,
			ConnectivityState: connectivityState,
			HealthStatus:      healthStatus,
		}
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
