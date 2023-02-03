package api

var sigStop = make(chan interface{})

func Stop() {
	sigStop <- struct{}{}
}
