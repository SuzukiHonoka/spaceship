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

	// Close the previous log file if one was open.
	if currentLogFile != nil {
		_ = currentLogFile.Close()
		currentLogFile = nil
	}

	switch m {
	case ModeDefault:
		log.Println("log will be redirected to stdout")
		log.SetOutput(os.Stdout)
	case ModeDiscard:
		log.Println("log disabled")
		log.SetOutput(io.Discard)
	case ModeSkip:
		return
	default:
		fd, err := os.OpenFile(string(m), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			log.Printf("Error when opening logfile: %v", err)
			ModeDefault.Set()
			return
		}
		currentLogFile = fd
		log.Printf("log will be saved to %s", m)
		log.SetOutput(fd)
	}
}
