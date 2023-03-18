package transport

import "errors"

var (
	ErrorPacketLoss      = errors.New("packet loss")
	ErrorUserNotFound    = errors.New("user not found")
	ErrorRequestNotFound = errors.New("request not found")
	ErrorTargetACKFailed = errors.New("target ack failed")
	ErrorServerFailed    = errors.New("server error")
	ErrorBadRequest      = errors.New("bad request")
	ErrorKeepAliveNeeded = errors.New("keep alive needed")
)
