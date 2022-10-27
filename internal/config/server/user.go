package server

type User struct {
	UUID  string `json:"uuid"` // user id
	Limit *struct {
		DownLink  uint64
		UpLink    uint64
		Bandwidth uint16
	} `json:"limit,omitempty"`
	Remark string `json:"remark,omitempty"`
}

type Users []User

func (u Users) IsNullOrEmpty() bool {
	return u == nil || len(u) == 0
}

func (u Users) Match(id string) bool {
	for _, user := range u {
		if user.UUID == id {
			return true
		}
	}
	return false
}
