package logger

import (
	"io"
	"log"
	"os"
)

type Mode string

const (
	ModeDiscard Mode = "null"
)

func (m Mode) Set() {
	switch m {
	case ModeDiscard:
		log.Println("log disabled")
		log.SetOutput(io.Discard)
	default:
		if m == "" {
			log.Println("log will be redirected to stdout")
			log.SetOutput(os.Stdout)
		} else {
			fd, err := os.OpenFile(string(m), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				log.Fatalf("Error when opening logfile: %s", err.Error())
			}
			log.Printf("log will be saved to %s", m)
			log.SetOutput(fd)
		}
	}
}
