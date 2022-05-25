package server

import "github.com/google/uuid"

type User struct {
	UUID  uuid.UUID // user id
	Limit *struct {
		DownLink  uint64
		UpLink    uint64
		Bandwidth uint16
	} `json:"limit,omitempty"`
	Remark string `json:"remark,omitempty"`
}
