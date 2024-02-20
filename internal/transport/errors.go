package transport

import "errors"

var (
	ErrPackageLoss     = errors.New("package loss")
	ErrUserNotFound    = errors.New("user not found")
	ErrRequestNotFound = errors.New("request not found")
	ErrTargetACKFailed = errors.New("target ack failed")
	ErrServerFailed    = errors.New("server error")
	ErrBadRequest      = errors.New("bad request")
	ErrKeepAliveNeeded = errors.New("keep alive needed")
	ErrBlocked         = errors.New("blocked")
	ErrNotImplemented  = errors.New("not implemented")
)
