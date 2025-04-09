package transport

import "errors"

var (
	ErrUserNotFound    = errors.New("user not found")
	ErrTargetACKFailed = errors.New("target ack failed")
	ErrServerError     = errors.New("server error")
	ErrBadRequest      = errors.New("bad request")
	ErrBlocked         = errors.New("blocked")
	ErrNotImplemented  = errors.New("not implemented")
	ErrInvalidMessage  = errors.New("invalid message")
	ErrInvalidPayload  = errors.New("invalid payload")
)
