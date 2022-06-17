package transport

import "errors"

var (
	ErrorPacketLoss      = errors.New("paket loss")
	ErrorUserNotFound    = errors.New("user not found")
	ErrorRequestNotFound = errors.New("request not found")
	ErrorTargetACKFailed = errors.New("target ack failed")
	ErrorServerFailed    = errors.New("server error")
	ErrorBadRequest      = errors.New("bad request")
)
