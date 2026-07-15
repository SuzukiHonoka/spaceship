package logger

import (
	"io"
	"log"
	"os"
	"sync"
)

type Mode string

const (
	ModeDefault Mode = ""
	ModeDiscard Mode = "null"
	ModeSkip    Mode = "skip"
)

var (
	// currentLogFile tracks the open log file so it can be closed on reconfiguration.
	currentLogFile *os.File
	logMu          sync.Mutex
)

func (m Mode) Set() {
	logMu.Lock()
	defer logMu.Unlock()

	switch m {
	case ModeDefault:
		setDefaultLocked()
	case ModeDiscard:
		log.Println("log disabled")
		log.SetOutput(io.Discard)
		closeCurrentLocked()
	case ModeSkip:
		return
	default:
		fd, err := os.OpenFile(string(m), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			log.Printf("Error when opening logfile: %v", err)
			setDefaultLocked()
			return
		}
		old := currentLogFile
		currentLogFile = fd
		log.SetOutput(fd)
		if old != nil {
			_ = old.Close()
		}
		log.Printf("log will be saved to %s", m)
	}
}

func closeCurrentLocked() {
	if currentLogFile != nil {
		_ = currentLogFile.Close()
		currentLogFile = nil
	}
}

func setDefaultLocked() {
	log.SetOutput(os.Stdout)
	closeCurrentLocked()
	log.Println("log will be redirected to stdout")
}
