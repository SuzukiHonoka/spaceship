package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestCopyWithContextCompletes(t *testing.T) {
	var dst bytes.Buffer
	if err := CopyWithContext(context.Background(), nil, &dst, strings.NewReader("payload"), DirectionOut); err != nil {
		t.Fatal(err)
	}
	if got := dst.String(); got != "payload" {
		t.Fatalf("copied data = %q, want payload", got)
	}
}

type blockingReader struct {
	started chan struct{}
	done    chan struct{}
	once    sync.Once
}

func (r *blockingReader) Read([]byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.done
	return 0, io.ErrClosedPipe
}

func TestCopyWithContextCancellationUnblocksCopy(t *testing.T) {
	reader := &blockingReader{started: make(chan struct{}), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- CopyWithContext(ctx, func() { close(reader.done) }, io.Discard, reader, DirectionIn)
	}()

	<-reader.started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyWithContext() error = %v, want context.Canceled", err)
	}
}

type closeWriteRecorder struct {
	closeWriteCalls int
	closeCalls      int
}

func (r *closeWriteRecorder) CloseWrite() error {
	r.closeWriteCalls++
	return nil
}

func (r *closeWriteRecorder) Close() error {
	r.closeCalls++
	return nil
}

type closeRecorder struct{ calls int }

func (r *closeRecorder) Close() error {
	r.calls++
	return nil
}

func TestCloseHelpers(t *testing.T) {
	halfCloser := new(closeWriteRecorder)
	CloseWriteOrClose(halfCloser)
	if halfCloser.closeWriteCalls != 1 || halfCloser.closeCalls != 0 {
		t.Fatalf("half close calls = (%d, %d), want (1, 0)", halfCloser.closeWriteCalls, halfCloser.closeCalls)
	}

	first := new(closeRecorder)
	second := new(closeRecorder)
	CloseWriteOrClose(first)
	CloseAll(first, second, struct{}{})
	if first.calls != 2 || second.calls != 1 {
		t.Fatalf("close calls = (%d, %d), want (2, 1)", first.calls, second.calls)
	}
}
