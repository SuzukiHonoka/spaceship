package client

import (
	"testing"

	"google.golang.org/grpc/connectivity"
)

func TestPicker(t *testing.T) {
	const limit = 10
	type c struct {
		state connectivity.State
		load  uint32
	}
	for _, tc := range []struct {
		name  string
		conns []c
		want  int // index into conns, -1 for none
	}{
		{
			name:  "least-loaded ready",
			conns: []c{{connectivity.Ready, 3}, {connectivity.Ready, 1}},
			want:  1,
		},
		{
			// A reset connection lost its sessions and reads as the emptiest.
			name:  "ready beats an emptier reconnecting connection",
			conns: []c{{connectivity.Idle, 0}, {connectivity.Ready, 5}, {connectivity.Connecting, 0}},
			want:  1,
		},
		{
			name:  "idle with capacity beats a full ready connection",
			conns: []c{{connectivity.Ready, limit}, {connectivity.Idle, 2}},
			want:  1,
		},
		{
			// The caller grows the pool from a full live connection; a
			// connection in transient failure would fail the session at once.
			name:  "full live connection beats transient failure with capacity",
			conns: []c{{connectivity.TransientFailure, 0}, {connectivity.Ready, limit + 1}, {connectivity.Ready, limit}},
			want:  2,
		},
		{
			name:  "transient failure only when nothing is live",
			conns: []c{{connectivity.Shutdown, 0}, {connectivity.TransientFailure, 4}, {connectivity.TransientFailure, 2}},
			want:  2,
		},
		{
			name:  "shut down is never picked",
			conns: []c{{connectivity.Shutdown, 0}},
			want:  -1,
		},
		{
			name:  "ties keep pool order",
			conns: []c{{connectivity.Ready, 1}, {connectivity.Ready, 1}},
			want:  0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wrappers := make([]*ConnWrapper, len(tc.conns))
			p := picker{limit: limit}
			for i, conn := range tc.conns {
				wrappers[i] = &ConnWrapper{ID: i + 1}
				p.consider(wrappers[i], conn.state, conn.load)
			}
			got := p.choice()
			var want *ConnWrapper
			if tc.want >= 0 {
				want = wrappers[tc.want]
			}
			if got != want {
				t.Fatalf("picked %v, want %v", describe(got), describe(want))
			}
		})
	}
}

func describe(w *ConnWrapper) any {
	if w == nil {
		return "none"
	}
	return w.ID
}
