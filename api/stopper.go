package api

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
)

var ErrSignalArrived = errors.New("signal arrived")

func (l *Launcher) listenSignal(ctx context.Context) error {
	sys := make(chan os.Signal, 1)
	signal.Notify(sys, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-sys:
	case <-l.sigStop:
	case <-ctx.Done():
		return ctx.Err()
	}
	log.Println("signal arrived")
	return ErrSignalArrived
}

func (l *Launcher) Stop() {
	close(l.sigStop)
}
