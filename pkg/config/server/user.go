package server

type User struct {
	UUID   string `json:"uuid"` // user id
	Limit  *Limit `json:"limit,omitempty"`
	Remark string `json:"remark,omitempty"`
}

type Users []*User

// Match returns true if the user id is in the users list.
// Deprecated: use UsersMatchMap.Match for O(1) lookup.
func (u Users) Match(id string) bool {
	for _, user := range u {
		if user.UUID == id {
			return true
		}
	}
	return false
}

func (u Users) ToMatchMap() *UsersMatchMap {
	return NewUsersMatchMap(u)
}

// UsersMatchMap provides O(1) user lookup. Immutable after creation.
type UsersMatchMap struct {
	m map[string]struct{}
}

func NewUsersMatchMap(users Users) *UsersMatchMap {
	m := make(map[string]struct{}, len(users))
	for _, user := range users {
		m[user.UUID] = struct{}{}
	}
	return &UsersMatchMap{m: m}
}

func (m *UsersMatchMap) Match(id string) bool {
	_, ok := m.m[id]
	return ok
}
