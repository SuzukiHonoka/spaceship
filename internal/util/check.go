package util

import "log"

func StopIfError(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
