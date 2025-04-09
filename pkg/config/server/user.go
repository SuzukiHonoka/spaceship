package server

import "sync"

type User struct {
	UUID   string `json:"uuid"` // user id
	Limit  *Limit `json:"limit,omitempty"`
	Remark string `json:"remark,omitempty"`
}

type Users []*User

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

func (u Users) ToMatchMap() *UsersMatchMap {
	return NewUsersMatchMap(u)
}

type UsersMatchMap struct {
	m sync.Map
}

func NewUsersMatchMap(users Users) *UsersMatchMap {
	m := new(UsersMatchMap)
	for _, user := range users {
		m.m.Store(user.UUID, struct{}{})
	}
	return m
}

func (m *UsersMatchMap) Match(id string) bool {
	_, ok := m.m.Load(id)
	return ok
}
