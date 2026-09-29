package benchtest

import "testing"

func TestByteSink(t *testing.T) {
	s := &byteSink{done: make(chan struct{}, 1)}
	for range 2 {
		s.remaining.Store(8)
		for _, n := range []int{0, 3, 5} {
			if written, err := s.Write(make([]byte, n)); written != n || err != nil {
				t.Fatalf("Write(%d) = %d, %v", n, written, err)
			}
		}
		if len(s.done) != 1 {
			t.Fatal("expected exactly one completion")
		}
		<-s.done
	}
	if _, err := s.Write([]byte{1}); err == nil {
		t.Fatal("accepted excess bytes")
	}
}
