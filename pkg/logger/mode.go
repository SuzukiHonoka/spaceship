package logger

import (
	"io"
	"log"
	"os"
)

type Mode string

const (
	ModeDefault Mode = ""
	ModeDiscard Mode = "null"
)

func (m Mode) Set() {
	switch m {
	case ModeDefault:
		log.Println("log will be redirected to stdout")
		log.SetOutput(os.Stdout)
	case ModeDiscard:
		log.Println("log disabled")
		log.SetOutput(io.Discard)
	default:
		fd, err := os.OpenFile(string(m), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			log.Printf("Error when opening logfile: %v", err)
			ModeDefault.Set()
			return
		}
		log.Printf("log will be saved to %s", m)
		log.SetOutput(fd)
	}
}
