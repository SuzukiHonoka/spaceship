package server

type User struct {
	Remark string `json:"remark,omitempty"`
	UUID   string `json:"uuid"` // user id
	Limit  *Limit `json:"limit,omitempty"`
}

type Users []User

func (u Users) IsNullOrEmpty() bool {
	return u == nil || len(u) == 0
}

// Match returns true if the user id is in the users list
// may use map for fast lookup in the future
func (u Users) Match(id string) bool {
	for _, user := range u {
		if user.UUID == id {
			return true
		}
	}
	return false
}
