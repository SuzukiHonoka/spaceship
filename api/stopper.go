package api

import (
	"os"
	"os/signal"
	"syscall"
)

func (l *Launcher) waitForCancel() {
	sys := make(chan os.Signal, 1)
	signal.Notify(sys, os.Interrupt, syscall.SIGKILL, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-sys:
	case <-l.sigStop:
	}
}

func (l *Launcher) Stop() {
	l.sigStop <- struct{}{}
}
