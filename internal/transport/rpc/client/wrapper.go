package client

import (
	"fmt"
	"log"
	"sync/atomic"

	"google.golang.org/grpc"
)

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
