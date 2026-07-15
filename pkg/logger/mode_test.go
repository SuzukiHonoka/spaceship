package logger

import (
	"log"
	"testing"
	"time"
)

func TestModeSetInvalidPathFallsBackWithoutDeadlock(t *testing.T) {
	oldWriter := log.Writer()
	defer log.SetOutput(oldWriter)

	done := make(chan struct{})
	go func() {
		Mode(t.TempDir()).Set()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Mode.Set deadlocked on invalid log path")
	}
}
