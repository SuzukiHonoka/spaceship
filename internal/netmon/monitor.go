package netmon

import (
	"errors"
	"net/netip"
)

// ErrNoDefaultInterface is returned when no physical default egress interface
// can be detected.
var ErrNoDefaultInterface = errors.New("netmon: no default physical interface found")

// DefaultInterface describes the active physical egress interface used to
// bypass a catch-all TUN route without looping.
type DefaultInterface struct {
	Index    uint32
	Name     string
	Gateway4 netip.Addr
	Gateway6 netip.Addr
}

// Equal reports whether two interface snapshots refer to the same egress.
func (d DefaultInterface) Equal(other DefaultInterface) bool {
	return d.Index == other.Index &&
		d.Name == other.Name &&
		d.Gateway4 == other.Gateway4 &&
		d.Gateway6 == other.Gateway6
}

// Monitor watches the host routing table for default egress interface changes,
// ignoring the TUN interface named excludeTUN.
type Monitor interface {
	Current() (DefaultInterface, error)
	Start(onChange func(DefaultInterface)) error
	Close() error
}

// NewMonitor creates a platform-specific default interface monitor that skips
// excludeTUN (when non-empty).
func NewMonitor(excludeTUN string) (Monitor, error) {
	return newPlatformMonitor(excludeTUN)
}
