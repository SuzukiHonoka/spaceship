package api

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

func (l *Launcher) listenSignal() {
	sys := make(chan os.Signal, 1)
	signal.Notify(sys, os.Interrupt, syscall.SIGKILL, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-sys:
	}
	log.Println("signal arrived")
	l.Stop()
}

func (l *Launcher) Stop() {
	l.sigStop <- struct{}{}
}
